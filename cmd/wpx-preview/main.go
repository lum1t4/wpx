// wpx-preview renders the real panel with disposable sample data. It never starts
// a worker, connects to a privileged broker, or loads an installed configuration.
// Keep synthetic authentication here, outside the production executable.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
	"github.com/lum1t4/wpx/internal/web"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9080", "HTTP preview address; use a loopback-only host port when running in Docker")
	flag.Parse()
	if err := run(*listen); err != nil {
		fmt.Fprintln(os.Stderr, "wpx-preview:", err)
		os.Exit(1)
	}
}

func run(listen string) error {
	directory, err := os.MkdirTemp("", "wpx-preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	statePath := filepath.Join(directory, "state.db")
	state, err := store.Open(statePath)
	if err != nil {
		return err
	}
	defer state.Close()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := state.ConfigureSecretKey(key); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	owner, err := seed(ctx, state, statePath)
	if err != nil {
		return fmt.Errorf("create fixtures: %w", err)
	}
	token, err := state.CreateSession(ctx, owner.ID, 24*time.Hour)
	if err != nil {
		return err
	}
	panel, err := web.New(config.Config{StatePath: statePath, DataRoot: directory, SiteRoot: filepath.Join(directory, "sites")}, state, sampleBroker{}, slog.Default())
	if err != nil {
		return err
	}
	server := &http.Server{Addr: listen, Handler: readOnly(panel.Handler(), token), ReadHeaderTimeout: 5 * time.Second}
	go panel.MonitorResources(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("WPX preview: http://%s — sample data, read-only; Ctrl+C removes the temporary state.\n", listen)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func seed(ctx context.Context, state *store.Store, path string) (store.User, error) {
	owner, err := state.CreateOwner(ctx, "alex", rand.Text())
	if err != nil {
		return owner, err
	}
	sites := []model.Site{
		{ID: "northstar", Domain: "northstar.example.com", Kind: model.WordPress, PHPVersion: "8.4"},
		{ID: "northstar-staging", Domain: "staging.northstar.example.com", Kind: model.WordPress, PHPVersion: "8.4"},
		{ID: "studio", Domain: "studio.example.com", Kind: model.Static},
		{ID: "customer-portal", Domain: "portal.example.com", Kind: model.PHP, PHPVersion: "8.3"},
		{ID: "analytics", Domain: "analytics.example.com", Kind: model.Python},
	}
	for _, site := range sites {
		if _, err := state.CreateSite(ctx, owner, site); err != nil {
			return owner, err
		}
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{Name: "Offsite backups", Endpoint: "https://storage.example.com", Bucket: "wpx-preview", Region: "eu-west-1", AccessKey: "sample-access-key", SecretKey: "sample-secret-key"})
	if err != nil {
		return owner, err
	}
	// Only the fresh database can reach this connection. SQL supplies finished
	// operational states without invoking the real worker or any provider.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return owner, err
	}
	defer db.Close()
	for _, query := range []string{
		"UPDATE sites SET status='active',tls_status='active'",
		"UPDATE sites SET environment='staging',parent_site_id='northstar' WHERE id='northstar-staging'",
		"UPDATE sites SET status='failed',tls_status='none' WHERE id='customer-portal'",
		"UPDATE sites SET status='disabled' WHERE id='analytics'",
		"UPDATE backup_targets SET status='active'",
		"UPDATE jobs SET status='succeeded',phase='complete',progress=100,finished_at=updated_at",
		"UPDATE jobs SET status='failed',error='Sample failure: PHP configuration could not be validated. Review the site settings and retry.' WHERE target_id='customer-portal'",
		"INSERT INTO database_admin(id,status,updated_at) VALUES(1,'active',CURRENT_TIMESTAMP)",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			return owner, err
		}
	}
	database, _, err := state.CreateDatabase(ctx, owner, "northstar", "Membership data")
	if err != nil {
		return owner, err
	}
	if _, err := db.ExecContext(ctx, "UPDATE databases SET status='active' WHERE id=?", database.ID); err != nil {
		return owner, err
	}
	if _, err := db.ExecContext(ctx, "UPDATE jobs SET status='succeeded',phase='complete',progress=100,finished_at=updated_at WHERE target_id=?", database.ID); err != nil {
		return owner, err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO backup_snapshots(id,site_id,target_id,restic_snapshot_id,created_at) VALUES(?,?,?,?,?)", "snapshot-preview", "northstar", target.ID, strings.Repeat("a", 64), time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	return owner, err
}

func readOnly(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-WPX-Preview", "sample-data")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Preview is read-only. No changes were made.", http.StatusMethodNotAllowed)
			return
		}
		r.Header.Del("Cookie")
		r.AddCookie(&http.Cookie{Name: "wpx_session", Value: token})
		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, r)
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		body := recorder.Body.Bytes()
		if strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			banner := []byte(`<div role="status" class="border-b border-zinc-200 bg-zinc-50 px-5 py-3 text-center text-sm text-zinc-500">Preview · sample data · read-only</div><main`)
			body = bytes.Replace(body, []byte("<main"), banner, 1)
			w.Header().Del("Content-Length")
		}
		w.WriteHeader(recorder.Code)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

type sampleBroker struct{}

func (sampleBroker) Call(_ context.Context, operation broker.Operation, _ string, _ any, result any) error {
	switch operation {
	case broker.OpWordPressInventory:
		*result.(*broker.WordPressInventoryResult) = broker.WordPressInventoryResult{CoreVersion: "6.8.2", Plugins: []broker.WordPressPlugin{{Name: "redis-cache", Status: "active", Version: "2.6.3"}, {Name: "woocommerce", Status: "active", Version: "9.9.5", Update: "available", UpdateVersion: "9.9.6"}, {Name: "hello-dolly", Status: "inactive", Version: "1.7.2"}}, Themes: []broker.WordPressTheme{{Name: "twentytwentyfive", Status: "active", Version: "1.2"}}}
	case broker.OpFileList:
		*result.(*broker.FileListResult) = broker.FileListResult{Entries: []broker.FileEntry{{Name: "wp-content", Path: "wp-content", IsDir: true}, {Name: "index.php", Path: "index.php", Size: 405}, {Name: "robots.txt", Path: "robots.txt", Size: 68}}}
	case broker.OpFileRead:
		*result.(*broker.FileReadResult) = broker.FileReadResult{Content: "<?php\n// Preview sample. Editing is disabled.\nrequire __DIR__ . '/wp-blog-header.php';\n"}
	case broker.OpSiteObservability:
		*result.(*broker.SiteObservabilityResult) = broker.SiteObservabilityResult{DiskBytes: 342884352, FileCount: 8421, AccessLog: []string{`192.0.2.10 - - [06/Sep/2026:10:42:08 +0000] "GET / HTTP/1.1" 200 28431`, `192.0.2.11 - - [06/Sep/2026:10:42:12 +0000] "GET /about/ HTTP/1.1" 200 12310`}}
	case broker.OpWordPressHealth:
		*result.(*broker.WordPressHealthResult) = broker.WordPressHealthResult{Checks: []broker.WordPressHealthCheck{{Name: "Database connection", Status: "passed"}, {Name: "Front page", Status: "passed"}}}
	case broker.OpInspectStaging:
		*result.(*broker.StagingInspection) = broker.StagingInspection{Files: []broker.StagingFileChange{{Path: "wp-content/themes/twentytwentyfive/style.css", Status: "modified", Bytes: 2014}}, Tables: []string{"wp_posts", "wp_options"}}
	default:
		return fmt.Errorf("operation %s is not available in the read-only preview", operation)
	}
	return nil
}
