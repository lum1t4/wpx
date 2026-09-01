// Package web is the unprivileged control plane. Handlers validate user intent,
// persist desired state, and call typed broker operations. They never invoke a
// package manager, service command, or privileged filesystem operation directly.
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
	"github.com/lum1t4/wpx/internal/updatecheck"
)

//go:embed templates/*.html static/*
var templateFiles embed.FS

type Server struct {
	cfg       config.Config
	store     *store.Store
	templates *template.Template
	logger    *slog.Logger
	broker    brokerCaller
}

type brokerCaller interface {
	Call(context.Context, broker.Operation, string, any, any) error
}

type pageData struct {
	Title              string
	User               *store.User
	CSRF               string
	Error              string
	Message            string
	Sites              []model.Site
	SetupOpen          bool
	TOTPSecret         string
	TOTPUri            string
	RecoveryCodes      []string
	Users              []store.User
	CanManageSites     bool
	CanManageUsers     bool
	Site               *model.Site
	CanWordPressLogin  bool
	CanManageTLS       bool
	CanManageWordPress bool
	Plugins            []broker.WordPressPlugin
	Themes             []broker.WordPressTheme
	CoreVersion        string
	CoreUpdateVersion  string
	CanManageFiles     bool
	Files              []broker.FileEntry
	FilePath           string
	FileContent        string
	ParentPath         string
	EditingFile        bool
	CanManageServer    bool
	CanManageBackups   bool
	CanDeploySite      bool
	CanViewLogs        bool
	StagingSites       []model.Site
	ProductionSite     *model.Site
	StagingUsername    string
	StagingPassword    string
	Observability      *broker.SiteObservabilityResult
	CanManageDNS       bool
	DNSProviders       []model.DNSProvider
	DNSRecords         []model.DNSRecord
	BackupTargets      []model.BackupTarget
	BackupSnapshots    []model.BackupSnapshot
	BackupSchedules    []model.BackupSchedule
	BackupPassword     string
	StagingInspection  *broker.StagingInspection
	WordPressHealth    *broker.WordPressHealthResult
	UpdateStatus       *store.UpdateStatus
	UpdateAvailable    bool
	SiteSnippets       *model.SiteSnippets
	Jobs               []store.JobSummary
	ActiveJobs         bool
	SelectedUser       *store.User
}

func New(cfg config.Config, state *store.Store, privileged brokerCaller, logger *slog.Logger) (*Server, error) {
	tmpl, err := template.New("wpx").Funcs(template.FuncMap{"bytes": humanBytes, "jobLabel": jobLabel, "assigned": userAssignedSite}).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse web templates: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{cfg: cfg, store: state, templates: tmpl, logger: logger, broker: privileged}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	staticFS, err := fs.Sub(templateFiles, "static")
	if err != nil {
		panic("embedded static filesystem is invalid: " + err.Error())
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("GET /login/totp", s.loginTOTPPage)
	mux.HandleFunc("POST /login/totp", s.loginTOTPSubmit)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /", s.requireSession(s.dashboard))
	mux.HandleFunc("GET /jobs", s.requireSession(s.jobsPage))
	mux.HandleFunc("GET /sites", s.requireSession(s.sitesPage))
	mux.HandleFunc("POST /sites", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.createSite)))
	mux.HandleFunc("GET /sites/{id}", s.requireSession(s.sitePage))
	mux.HandleFunc("POST /sites/{id}/disable", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.disableSite)))
	mux.HandleFunc("POST /sites/{id}/enable", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.enableSite)))
	mux.HandleFunc("POST /sites/{id}/retry", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.retrySiteProvision)))
	mux.HandleFunc("POST /sites/{id}/expert-config", s.requireSession(s.requireCapability(rbac.ManageAllSites, s.saveSiteSnippets)))
	mux.HandleFunc("POST /sites/{id}/wordpress/login", s.requireSession(s.wordpressLogin))
	mux.HandleFunc("POST /sites/{id}/wordpress/performance", s.requireSession(s.setWordPressPerformance))
	mux.HandleFunc("POST /sites/{id}/wordpress/update", s.requireSession(s.updateWordPress))
	mux.HandleFunc("GET /sites/{id}/wordpress/health", s.requireSession(s.wordpressHealthPage))
	mux.HandleFunc("POST /sites/{id}/certificate", s.requireSession(s.issueCertificate))
	mux.HandleFunc("POST /sites/{id}/backups", s.requireSession(s.createSiteBackup))
	mux.HandleFunc("POST /sites/{id}/backups/schedule", s.requireSession(s.setSiteBackupSchedule))
	mux.HandleFunc("POST /sites/{id}/backups/{snapshot}/restore", s.requireSession(s.restoreSiteBackup))
	mux.HandleFunc("POST /sites/{id}/backups/{snapshot}/clone", s.requireSession(s.restoreSiteBackupClone))
	mux.HandleFunc("POST /sites/{id}/backups/restore-test", s.requireSession(s.testSiteBackupRestore))
	mux.HandleFunc("POST /sites/{id}/backups/restore-test-schedule", s.requireSession(s.setRestoreTestSchedule))
	mux.HandleFunc("POST /sites/{id}/staging", s.requireSession(s.createStaging))
	mux.HandleFunc("POST /sites/{id}/staging/sync", s.requireSession(s.syncStaging))
	mux.HandleFunc("GET /sites/{id}/staging/deploy", s.requireSession(s.stagingDeployPage))
	mux.HandleFunc("POST /sites/{id}/staging/deploy", s.requireSession(s.deployStaging))
	mux.HandleFunc("GET /sites/{id}/observability", s.requireSession(s.observabilityPage))
	mux.HandleFunc("GET /sites/{id}/dns", s.requireSession(s.siteDNSPage))
	mux.HandleFunc("POST /sites/{id}/dns", s.requireSession(s.createSiteDNSRecord))
	mux.HandleFunc("POST /sites/{id}/dns/{record}", s.requireSession(s.updateSiteDNSRecord))
	mux.HandleFunc("POST /sites/{id}/dns/{record}/delete", s.requireSession(s.deleteSiteDNSRecord))
	mux.HandleFunc("POST /sites/{id}/wordpress/plugins/{plugin}", s.requireSession(s.setWordPressPlugin))
	mux.HandleFunc("GET /sites/{id}/files", s.requireSession(s.filesPage))
	mux.HandleFunc("POST /sites/{id}/files", s.requireSession(s.saveFile))
	mux.HandleFunc("GET /account/security", s.requireSession(s.securityPage))
	mux.HandleFunc("POST /account/totp/begin", s.requireSession(s.beginTOTP))
	mux.HandleFunc("POST /account/totp/confirm", s.requireSession(s.confirmTOTP))
	mux.HandleFunc("POST /account/totp/disable", s.requireSession(s.disableTOTP))
	mux.HandleFunc("POST /account/password", s.requireSession(s.changePassword))
	mux.HandleFunc("GET /users", s.requireSession(s.requireCapability(rbac.ManageUsers, s.usersPage)))
	mux.HandleFunc("POST /users", s.requireSession(s.requireCapability(rbac.ManageUsers, s.createUser)))
	mux.HandleFunc("GET /users/{id}", s.requireSession(s.requireCapability(rbac.ManageUsers, s.editUserPage)))
	mux.HandleFunc("POST /users/{id}", s.requireSession(s.requireCapability(rbac.ManageUsers, s.updateUserAccess)))
	mux.HandleFunc("POST /users/{id}/status", s.requireSession(s.requireCapability(rbac.ManageUsers, s.setUserStatus)))
	mux.HandleFunc("GET /backups/targets", s.requireSession(s.requireCapability(rbac.ManageServer, s.backupTargetsPage)))
	mux.HandleFunc("POST /backups/targets", s.requireSession(s.requireCapability(rbac.ManageServer, s.createBackupTarget)))
	mux.HandleFunc("GET /dns/providers", s.requireSession(s.requireCapability(rbac.ManageServer, s.dnsProvidersPage)))
	mux.HandleFunc("POST /dns/providers", s.requireSession(s.requireCapability(rbac.ManageServer, s.createDNSProvider)))
	return s.securityHeaders(s.recoverPanics(mux))
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	httpServer := &http.Server{
		Addr: s.cfg.ListenAddress, Handler: s.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 1 << 20,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	err := httpServer.ListenAndServeTLS(s.cfg.TLSCertPath, s.cfg.TLSKeyPath)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Health(ctx); err != nil {
		http.Error(w, "state unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	exists, err := s.store.OwnerExists(r.Context())
	if err != nil {
		http.Error(w, "state unavailable", http.StatusServiceUnavailable)
		return
	}
	if exists {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "setup.html", pageData{Title: "Create owner", CSRF: s.ensureCSRF(w, r), SetupOpen: true})
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	exists, err := s.store.OwnerExists(r.Context())
	if err != nil || exists {
		http.Error(w, "setup is closed", http.StatusForbidden)
		return
	}
	provided := sha256.Sum256([]byte(r.FormValue("bootstrap_token")))
	expected, err := hex.DecodeString(s.cfg.BootstrapTokenHash)
	if err != nil || len(expected) != sha256.Size || subtle.ConstantTimeCompare(provided[:], expected) != 1 {
		s.renderStatus(w, "setup.html", http.StatusUnauthorized, pageData{Title: "Create owner", CSRF: s.ensureCSRF(w, r), Error: "The bootstrap token is invalid.", SetupOpen: true})
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		s.renderStatus(w, "setup.html", http.StatusBadRequest, pageData{Title: "Create owner", CSRF: s.ensureCSRF(w, r), Error: "The passwords do not match.", SetupOpen: true})
		return
	}
	user, err := s.store.CreateOwner(r.Context(), strings.TrimSpace(r.FormValue("username")), password)
	if err != nil {
		s.renderStatus(w, "setup.html", http.StatusBadRequest, pageData{Title: "Create owner", CSRF: s.ensureCSRF(w, r), Error: err.Error(), SetupOpen: true})
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, 30*time.Minute)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	s.setSession(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	exists, _ := s.store.OwnerExists(r.Context())
	if !exists {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", pageData{Title: "Sign in", CSRF: s.ensureCSRF(w, r)})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	user, err := s.store.Authenticate(r.Context(), strings.TrimSpace(r.FormValue("username")), r.FormValue("password"))
	if err != nil {
		// A small fixed delay makes online guessing more expensive without exposing
		// whether bcrypt or the account lookup caused the rejection.
		time.Sleep(250 * time.Millisecond)
		s.renderStatus(w, "login.html", http.StatusUnauthorized, pageData{Title: "Sign in", CSRF: s.ensureCSRF(w, r), Error: "The username or password is incorrect."})
		return
	}
	if user.TOTPEnabled {
		challenge, err := s.store.CreateLoginChallenge(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "could not create login challenge", http.StatusInternalServerError)
			return
		}
		s.setLoginChallenge(w, challenge)
		http.Redirect(w, r, "/login/totp", http.StatusSeeOther)
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, 30*time.Minute)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	s.setSession(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginTOTPPage(w http.ResponseWriter, r *http.Request) {
	challenge, user, err := s.loginChallenge(r)
	if err != nil {
		s.clearLoginChallenge(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	_ = challenge
	s.render(w, "login_totp.html", pageData{Title: "Verify sign-in", User: &user, CSRF: s.ensureCSRF(w, r)})
}

func (s *Server) loginTOTPSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	challenge, user, err := s.loginChallenge(r)
	if err != nil {
		s.clearLoginChallenge(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := s.store.VerifySecondFactor(r.Context(), user.ID, r.FormValue("code")); err != nil {
		time.Sleep(250 * time.Millisecond)
		s.renderStatus(w, "login_totp.html", http.StatusUnauthorized, pageData{Title: "Verify sign-in", User: &user, CSRF: s.ensureCSRF(w, r), Error: "The authenticator or recovery code is invalid."})
		return
	}
	if err := s.store.DeleteLoginChallenge(r.Context(), challenge); err != nil {
		http.Error(w, "could not complete login", http.StatusInternalServerError)
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, 30*time.Minute)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	s.clearLoginChallenge(w)
	s.setSession(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginChallenge(r *http.Request) (string, store.User, error) {
	cookie, err := r.Cookie("wpx_login")
	if err != nil {
		return "", store.User{}, err
	}
	user, err := s.store.ResolveLoginChallenge(r.Context(), cookie.Value)
	return cookie.Value, user, err
}

func (s *Server) setLoginChallenge(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: "wpx_login", Value: token, Path: "/login/totp", MaxAge: 300, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearLoginChallenge(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "wpx_login", Value: "", Path: "/login/totp", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie("wpx_session"); err == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "wpx_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, user store.User) {
	sites, err := s.store.ListSitesForUser(r.Context(), user)
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Overview", User: &user, CSRF: s.ensureCSRF(w, r), Sites: sites, CanManageSites: rbac.Allows(user.Role, rbac.ManageAllSites), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanManageServer: rbac.Allows(user.Role, rbac.ManageServer)}
	if data.CanManageServer {
		status, statusErr := s.store.UpdateStatus(r.Context())
		if statusErr != nil {
			http.Error(w, "could not load update status", http.StatusInternalServerError)
			return
		}
		if status.CheckedAt != "" {
			data.UpdateStatus = &status
			data.UpdateAvailable = updatecheck.IsNewer(status.CurrentVersion, status.LatestVersion)
		}
	}
	s.render(w, "dashboard.html", data)
}

func (s *Server) jobsPage(w http.ResponseWriter, r *http.Request, user store.User) {
	jobs, err := s.store.RecentJobsForUser(r.Context(), user, 100)
	if err != nil {
		http.Error(w, "could not load activity", http.StatusInternalServerError)
		return
	}
	active := false
	for _, job := range jobs {
		if job.Status == "queued" || job.Status == "running" {
			active = true
			break
		}
	}
	s.render(w, "jobs.html", pageData{Title: "Activity", User: &user, CSRF: s.ensureCSRF(w, r), Jobs: jobs, ActiveJobs: active, CanManageSites: rbac.Allows(user.Role, rbac.ManageAllSites), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanManageServer: rbac.Allows(user.Role, rbac.ManageServer)})
}

func (s *Server) sitesPage(w http.ResponseWriter, r *http.Request, user store.User) {
	sites, err := s.store.ListSitesForUser(r.Context(), user)
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	s.render(w, "sites.html", pageData{Title: "Sites", User: &user, CSRF: s.ensureCSRF(w, r), Sites: sites, CanManageSites: rbac.Allows(user.Role, rbac.ManageAllSites), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers)})
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site := model.Site{
		ID: strings.TrimSpace(r.FormValue("id")), Domain: strings.ToLower(strings.TrimSpace(r.FormValue("domain"))),
		Kind: model.SiteKind(r.FormValue("kind")), PHPVersion: r.FormValue("php_version"), Upstream: strings.TrimSpace(r.FormValue("upstream")), AllowEOL: r.FormValue("allow_eol") == "yes",
		WordPressMultisite: model.WordPressMultisiteMode(r.FormValue("wordpress_multisite")),
	}
	if site.Kind != model.WordPress && site.Kind != model.PHP {
		site.PHPVersion = ""
	}
	if site.Kind != model.WordPress {
		site.WordPressMultisite = model.MultisiteDisabled
	}
	if err := model.ValidateSite(site); err != nil {
		s.renderSiteError(w, r, user, err.Error())
		return
	}
	if _, err := s.store.CreateSite(r.Context(), user, site); err != nil {
		s.renderSiteError(w, r, user, err.Error())
		return
	}
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

func (s *Server) sitePage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ViewSite) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	data := pageData{Title: site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageSites: rbac.Allows(user.Role, rbac.ManageAllSites), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanWordPressLogin: site.Kind == model.WordPress && s.store.UserCanSite(r.Context(), user, site.ID, rbac.WordPressLogin), CanManageTLS: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageTLS), CanManageWordPress: site.Kind == model.WordPress && s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress), CanManageFiles: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageFiles), CanManageBackups: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageBackups), CanDeploySite: s.store.UserCanSite(r.Context(), user, site.ID, rbac.DeploySite), CanViewLogs: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ViewLogs), CanManageDNS: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageDNS)}
	if site.Kind == model.WordPress {
		sites, listErr := s.store.ListSitesForUser(r.Context(), user)
		if listErr != nil {
			http.Error(w, "could not load site environments", http.StatusInternalServerError)
			return
		}
		if site.Environment == "production" {
			for _, candidate := range sites {
				if candidate.ParentSiteID == site.ID {
					data.StagingSites = append(data.StagingSites, candidate)
				}
			}
		} else if site.ParentSiteID != "" {
			for _, candidate := range sites {
				if candidate.ID == site.ParentSiteID {
					production := candidate
					data.ProductionSite = &production
					break
				}
			}
		}
	}
	if data.CanManageTLS {
		providers, listErr := s.store.ListDNSProviders(r.Context())
		if listErr != nil {
			http.Error(w, "could not load DNS providers", http.StatusInternalServerError)
			return
		}
		data.DNSProviders = providers
	}
	if data.CanManageSites {
		snippets, snippetErr := s.store.SiteSnippets(r.Context(), site.ID)
		if snippetErr != nil {
			http.Error(w, "could not load expert configuration", http.StatusInternalServerError)
			return
		}
		data.SiteSnippets = &snippets
	}
	if r.URL.Query().Get("backup") == "queued" {
		data.Message = "Backup queued. It will appear under restore points when complete."
	}
	if r.URL.Query().Get("restore") == "queued" {
		data.Message = "Restore queued. WPX will create a rollback snapshot before switching the live site."
	}
	if r.URL.Query().Get("clone") == "queued" {
		data.Message = "Snapshot restore queued. WPX is preparing this independent site without changing the source."
	}
	if r.URL.Query().Get("lifecycle") == "queued" {
		data.Message = "Site lifecycle change queued. WPX will reconcile traffic and runtimes in the background."
	}
	if r.URL.Query().Get("config") == "queued" {
		data.Message = "Expert configuration queued. WPX will syntax-test it before reloading services."
	}
	if r.URL.Query().Get("restore-test") == "queued" {
		data.Message = "Restore test queued. WPX will download the snapshot and validate it without touching the live site."
	}
	if r.URL.Query().Get("deploy") == "queued" {
		data.Message = "Deployment queued. WPX will create an encrypted production recovery point before switching files and the database."
	}
	if r.URL.Query().Get("update") == "queued" {
		data.Message = "WordPress update queued. WPX will create an encrypted recovery point and run health checks before activating it."
	}
	if data.CanManageBackups || data.CanManageWordPress {
		data.BackupTargets, err = s.store.ListBackupTargets(r.Context())
		if err != nil {
			http.Error(w, "could not load backup targets", http.StatusInternalServerError)
			return
		}
		if data.CanManageBackups {
			data.BackupSnapshots, err = s.store.ListSiteSnapshots(r.Context(), site.ID)
			if err != nil {
				http.Error(w, "could not load backup snapshots", http.StatusInternalServerError)
				return
			}
			data.BackupSchedules, err = s.store.ListSiteBackupSchedules(r.Context(), site.ID)
			if err != nil {
				http.Error(w, "could not load backup schedules", http.StatusInternalServerError)
				return
			}
		}
	}
	if site.Kind == model.WordPress && site.Status == "active" && s.broker != nil {
		var result broker.WordPressInventoryResult
		if err := s.broker.Call(r.Context(), broker.OpWordPressInventory, "wordpress.inventory:"+site.ID+":"+mustRandomHex(8), broker.WordPressPluginsRequest{Site: site}, &result); err != nil {
			s.logger.Warn("load WordPress inventory", "site", site.ID, "error", err)
			data.Error = "WordPress is active, but its core, plugin, and theme inventory could not be loaded."
		} else {
			data.Plugins = result.Plugins
			data.Themes = result.Themes
			data.CoreVersion = result.CoreVersion
			data.CoreUpdateVersion = result.CoreUpdateVersion
		}
	}
	s.render(w, "site.html", data)
}

func (s *Server) disableSite(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("confirmation") != site.Domain {
		http.Error(w, "type the site domain to confirm", http.StatusBadRequest)
		return
	}
	if _, err := s.store.EnqueueSiteDisable(r.Context(), user, site.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"?lifecycle=queued", http.StatusSeeOther)
}

func (s *Server) enableSite(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.EnqueueSiteEnable(r.Context(), user, site.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"?lifecycle=queued", http.StatusSeeOther)
}

func (s *Server) retrySiteProvision(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.EnqueueSiteProvisionRetry(r.Context(), user, r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/sites/"+url.PathEscape(r.PathValue("id"))+"?provision=queued", http.StatusSeeOther)
}

func (s *Server) saveSiteSnippets(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	snippets := model.SiteSnippets{Nginx: r.FormValue("nginx"), PHP: r.FormValue("php")}
	if _, err := s.store.SetSiteSnippets(r.Context(), user, site.ID, snippets); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"?config=queued", http.StatusSeeOther)
}

func (s *Server) filesPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return
	}
	if s.broker == nil {
		http.Error(w, "file operations are unavailable", http.StatusServiceUnavailable)
		return
	}
	directory := r.URL.Query().Get("path")
	var list broker.FileListResult
	if err := s.broker.Call(r.Context(), broker.OpFileList, "file.list:"+site.ID+":"+mustRandomHex(8), broker.FileRequest{Site: site, Path: directory}, &list); err != nil {
		s.logger.Warn("list site files", "site", site.ID, "path", directory, "error", err)
		http.Error(w, "could not list this directory", http.StatusBadGateway)
		return
	}
	data := pageData{Title: "Files · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanManageFiles: true, Files: list.Entries, FilePath: directory, ParentPath: parentDirectory(directory)}
	if edit := r.URL.Query().Get("edit"); edit != "" {
		var result broker.FileReadResult
		if err := s.broker.Call(r.Context(), broker.OpFileRead, "file.read:"+site.ID+":"+mustRandomHex(8), broker.FileRequest{Site: site, Path: edit}, &result); err != nil {
			data.Error = "This file cannot be opened in the text editor."
		} else {
			data.EditingFile = true
			data.FilePath = edit
			data.FileContent = result.Content
		}
	}
	s.render(w, "files.html", data)
}

func (s *Server) saveFile(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return
	}
	path := r.FormValue("path")
	if s.broker == nil || s.broker.Call(r.Context(), broker.OpFileWrite, "file.write:"+site.ID+":"+mustRandomHex(12), broker.FileWriteRequest{Site: site, Path: path, Content: r.FormValue("content")}, nil) != nil {
		http.Error(w, "the file was not saved; check syntax and permissions", http.StatusBadRequest)
		return
	}
	directory := pathpkg.Dir(path)
	if directory == "." {
		directory = ""
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/files?path="+url.QueryEscape(directory)+"&edit="+url.QueryEscape(path), http.StatusSeeOther)
}

func (s *Server) authorizedSite(w http.ResponseWriter, r *http.Request, user store.User, capability rbac.Capability) (model.Site, bool) {
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return model.Site{}, false
	}
	if !s.store.UserCanSite(r.Context(), user, site.ID, capability) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return model.Site{}, false
	}
	return site, true
}

func (s *Server) backupTargetsPage(w http.ResponseWriter, r *http.Request, user store.User) {
	targets, err := s.store.ListBackupTargets(r.Context())
	if err != nil {
		http.Error(w, "could not load backup targets", http.StatusInternalServerError)
		return
	}
	s.render(w, "backup_targets.html", pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets})
}

func (s *Server) createBackupTarget(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	target := model.BackupTarget{
		Name: r.FormValue("name"), Endpoint: r.FormValue("endpoint"), Bucket: r.FormValue("bucket"),
		Prefix: r.FormValue("prefix"), Region: r.FormValue("region"), BucketLookup: r.FormValue("bucket_lookup"),
		AccessKey: r.FormValue("access_key"), SecretKey: r.FormValue("secret_key"), RepositoryPassword: r.FormValue("repository_password"),
		AllowInsecureHTTP: r.FormValue("allow_insecure_http") == "yes",
		DriveFolder:       r.FormValue("drive_folder"), GoogleClientID: r.FormValue("google_client_id"), GoogleClientSecret: r.FormValue("google_client_secret"), GoogleToken: r.FormValue("google_token"), GoogleSharedDrive: r.FormValue("google_shared_drive"),
	}
	var password string
	var err error
	if r.FormValue("kind") == string(model.BackupGoogleDrive) {
		_, password, err = s.store.CreateGoogleDriveTarget(r.Context(), user, target)
	} else {
		_, password, err = s.store.CreateS3Target(r.Context(), user, target)
	}
	if err != nil {
		targets, _ := s.store.ListBackupTargets(r.Context())
		s.renderStatus(w, "backup_targets.html", http.StatusBadRequest, pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets, Error: err.Error()})
		return
	}
	if password == "" {
		http.Redirect(w, r, "/backups/targets", http.StatusSeeOther)
		return
	}
	targets, _ := s.store.ListBackupTargets(r.Context())
	s.render(w, "backup_targets.html", pageData{Title: "Backup storage", User: &user, CSRF: s.ensureCSRF(w, r), BackupTargets: targets, BackupPassword: password, Message: "Backup storage is being verified. Save the generated repository password now; WPX will not show it again."})
}

func (s *Server) createSiteBackup(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	if _, err := s.store.EnqueueSiteBackup(r.Context(), user, site.ID, r.FormValue("target_id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"?backup=queued", http.StatusSeeOther)
}

func (s *Server) setSiteBackupSchedule(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	interval, err := strconv.Atoi(r.FormValue("interval_hours"))
	if err != nil {
		http.Error(w, "invalid backup frequency", http.StatusBadRequest)
		return
	}
	keepDaily, dailyErr := strconv.Atoi(r.FormValue("keep_daily"))
	keepWeekly, weeklyErr := strconv.Atoi(r.FormValue("keep_weekly"))
	keepMonthly, monthlyErr := strconv.Atoi(r.FormValue("keep_monthly"))
	if dailyErr != nil || weeklyErr != nil || monthlyErr != nil {
		http.Error(w, "invalid backup retention", http.StatusBadRequest)
		return
	}
	retention := model.BackupRetention{KeepDaily: keepDaily, KeepWeekly: keepWeekly, KeepMonthly: keepMonthly}
	restoreTestDays := 0
	if value := r.FormValue("restore_test_days"); value != "" {
		restoreTestDays, err = strconv.Atoi(value)
		if err != nil {
			http.Error(w, "invalid restore test frequency", http.StatusBadRequest)
			return
		}
	}
	if err := s.store.SetBackupSchedule(r.Context(), user, site.ID, r.FormValue("target_id"), interval, retention, restoreTestDays); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID, http.StatusSeeOther)
}

func (s *Server) restoreSiteBackup(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	if r.FormValue("confirmation") != site.Domain {
		http.Error(w, "type the site domain exactly to confirm the restore", http.StatusBadRequest)
		return
	}
	if _, err := s.store.EnqueueSiteRestore(r.Context(), user, site.ID, r.PathValue("snapshot")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"?restore=queued", http.StatusSeeOther)
}

func (s *Server) restoreSiteBackupClone(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	source, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	if !s.store.UserCanSite(r.Context(), user, source.ID, rbac.DeploySite) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	destination := r.FormValue("destination")
	targetID := strings.TrimSpace(r.FormValue("target_id"))
	targetDomain := strings.ToLower(strings.TrimSpace(r.FormValue("target_domain")))
	_, password, err := s.store.EnqueueRestoreClone(r.Context(), user, source.ID, r.PathValue("snapshot"), targetID, targetDomain, destination)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if destination == "staging" {
		parentID := source.ID
		if source.Environment == "staging" && source.ParentSiteID != "" {
			parentID = source.ParentSiteID
		}
		target := model.Site{ID: targetID, Domain: targetDomain, Kind: model.WordPress, Status: "queued", Environment: "staging", ParentSiteID: parentID}
		s.render(w, "staging_created.html", pageData{Title: "Staging restore queued", User: &user, CSRF: s.ensureCSRF(w, r), Site: &target, StagingUsername: "wpx", StagingPassword: password, Message: "The encrypted snapshot is being restored into this protected staging environment."})
		return
	}
	http.Redirect(w, r, "/sites/"+targetID+"?clone=queued", http.StatusSeeOther)
}

func (s *Server) testSiteBackupRestore(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	if _, err := s.store.EnqueueRestoreTest(r.Context(), user, site.ID, r.FormValue("snapshot_id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"?restore-test=queued", http.StatusSeeOther)
}

func (s *Server) setRestoreTestSchedule(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageBackups)
	if !ok {
		return
	}
	days, err := strconv.Atoi(r.FormValue("interval_days"))
	if err != nil {
		http.Error(w, "invalid restore test frequency", http.StatusBadRequest)
		return
	}
	if err := s.store.SetRestoreTestSchedule(r.Context(), user, site.ID, r.FormValue("target_id"), days); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID, http.StatusSeeOther)
}

func (s *Server) createStaging(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.DeploySite)
	if !ok {
		return
	}
	stagingID := strings.TrimSpace(r.FormValue("id"))
	stagingDomain := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))
	_, password, err := s.store.CreateStaging(r.Context(), user, site.ID, stagingID, stagingDomain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	staging := model.Site{ID: stagingID, Domain: stagingDomain, Kind: model.WordPress, Environment: "staging", ParentSiteID: site.ID, Status: "queued"}
	s.render(w, "staging_created.html", pageData{Title: "Staging created", User: &user, CSRF: s.ensureCSRF(w, r), Site: &staging, StagingUsername: "wpx", StagingPassword: password})
}

func (s *Server) syncStaging(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.DeploySite)
	if !ok {
		return
	}
	if site.Environment != "staging" || r.FormValue("confirmation") != site.Domain {
		http.Error(w, "type the staging domain exactly to confirm replacement", http.StatusBadRequest)
		return
	}
	if _, err := s.store.EnqueueStagingSync(r.Context(), user, site.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID, http.StatusSeeOther)
}

func (s *Server) deployStaging(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	staging, ok := s.authorizedSite(w, r, user, rbac.DeploySite)
	if !ok {
		return
	}
	if staging.Environment != "staging" {
		http.Error(w, "only staging can be deployed", http.StatusBadRequest)
		return
	}
	production, err := s.store.Site(r.Context(), staging.ParentSiteID)
	if err != nil || !s.store.UserCanSite(r.Context(), user, production.ID, rbac.DeploySite) {
		http.Error(w, "production site is unavailable", http.StatusForbidden)
		return
	}
	if r.FormValue("confirmation") != production.Domain {
		http.Error(w, "type the production domain exactly to confirm deployment", http.StatusBadRequest)
		return
	}
	selection := model.StagingSelection{Full: r.FormValue("mode") == "full"}
	if !selection.Full {
		selection.Files = append([]string(nil), r.Form["files"]...)
		selection.Tables = append([]string(nil), r.Form["tables"]...)
	}
	if _, err := s.store.EnqueueStagingDeploy(r.Context(), user, staging.ID, r.FormValue("target_id"), selection); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+production.ID+"?deploy=queued", http.StatusSeeOther)
}

func (s *Server) stagingDeployPage(w http.ResponseWriter, r *http.Request, user store.User) {
	staging, ok := s.authorizedSite(w, r, user, rbac.DeploySite)
	if !ok {
		return
	}
	if staging.Environment != "staging" || staging.Status != "active" {
		http.Error(w, "only an active staging site can be deployed", http.StatusBadRequest)
		return
	}
	production, err := s.store.Site(r.Context(), staging.ParentSiteID)
	if err != nil || !s.store.UserCanSite(r.Context(), user, production.ID, rbac.DeploySite) {
		http.Error(w, "production site is unavailable", http.StatusForbidden)
		return
	}
	if s.broker == nil {
		http.Error(w, "staging inspection is unavailable", http.StatusServiceUnavailable)
		return
	}
	var inspection broker.StagingInspection
	request := broker.InspectStagingRequest{Staging: staging, Production: production}
	if err := s.broker.Call(r.Context(), broker.OpInspectStaging, "wordpress.staging_inspect:"+staging.ID+":"+mustRandomHex(8), request, &inspection); err != nil {
		http.Error(w, "could not inspect staging changes", http.StatusBadGateway)
		return
	}
	targets, err := s.store.ListBackupTargets(r.Context())
	if err != nil {
		http.Error(w, "could not load recovery storage", http.StatusInternalServerError)
		return
	}
	s.render(w, "staging_deploy.html", pageData{Title: "Deploy " + staging.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &staging, ProductionSite: &production, BackupTargets: targets, CanDeploySite: true, StagingInspection: &inspection})
}

func (s *Server) observabilityPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ViewLogs)
	if !ok {
		return
	}
	if s.broker == nil {
		http.Error(w, "site observability is unavailable", http.StatusServiceUnavailable)
		return
	}
	var result broker.SiteObservabilityResult
	if err := s.broker.Call(r.Context(), broker.OpSiteObservability, "site.observability:"+site.ID+":"+mustRandomHex(8), broker.SiteObservabilityRequest{Site: site}, &result); err != nil {
		http.Error(w, "could not load local site data", http.StatusBadGateway)
		return
	}
	s.render(w, "observability.html", pageData{Title: "Logs · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanViewLogs: true, Observability: &result})
}

func (s *Server) dnsProvidersPage(w http.ResponseWriter, r *http.Request, user store.User) {
	providers, err := s.store.ListDNSProviders(r.Context())
	if err != nil {
		http.Error(w, "could not load DNS providers", http.StatusInternalServerError)
		return
	}
	s.render(w, "dns_providers.html", pageData{Title: "DNS providers", User: &user, CSRF: s.ensureCSRF(w, r), DNSProviders: providers})
}

func (s *Server) createDNSProvider(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	provider := model.DNSProvider{Name: r.FormValue("name"), Kind: model.DNSProviderKind(r.FormValue("kind")), ZoneID: r.FormValue("zone_id")}
	if provider.Kind == model.DNSCloudflare {
		provider.APIToken = r.FormValue("api_token")
	} else if provider.Kind == model.DNSRoute53 {
		provider.AccessKey, provider.SecretKey, provider.SessionToken = r.FormValue("access_key"), r.FormValue("secret_key"), r.FormValue("session_token")
	}
	if _, err := s.store.CreateDNSProvider(r.Context(), user, provider); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/dns/providers", http.StatusSeeOther)
}

func (s *Server) siteDNSPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageDNS)
	if !ok {
		return
	}
	providers, err := s.store.ListDNSProviders(r.Context())
	if err != nil {
		http.Error(w, "could not load DNS providers", http.StatusInternalServerError)
		return
	}
	records, err := s.store.ListSiteDNSRecords(r.Context(), site.ID)
	if err != nil {
		http.Error(w, "could not load DNS records", http.StatusInternalServerError)
		return
	}
	s.render(w, "site_dns.html", pageData{Title: "DNS · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageDNS: true, DNSProviders: providers, DNSRecords: records})
}

func (s *Server) createSiteDNSRecord(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageDNS)
	if !ok {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	ttl, err := strconv.Atoi(r.FormValue("ttl"))
	if err != nil {
		http.Error(w, "invalid TTL", http.StatusBadRequest)
		return
	}
	record := model.DNSRecord{SiteID: site.ID, ProviderID: r.FormValue("provider_id"), Name: r.FormValue("name"), Type: r.FormValue("type"), Value: r.FormValue("value"), TTL: ttl, Proxied: r.FormValue("proxied") == "yes"}
	if _, err := s.store.CreateDNSRecord(r.Context(), user, record); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/dns", http.StatusSeeOther)
}

func (s *Server) updateSiteDNSRecord(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageDNS)
	if !ok {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	ttl, err := strconv.Atoi(r.FormValue("ttl"))
	if err != nil {
		http.Error(w, "invalid TTL", http.StatusBadRequest)
		return
	}
	update := model.DNSRecord{Name: r.FormValue("name"), Type: r.FormValue("type"), Value: r.FormValue("value"), TTL: ttl, Proxied: r.FormValue("proxied") == "yes"}
	if _, err := s.store.UpdateDNSRecord(r.Context(), user, site.ID, r.PathValue("record"), update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/dns", http.StatusSeeOther)
}

func (s *Server) deleteSiteDNSRecord(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageDNS)
	if !ok {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.DeleteDNSRecord(r.Context(), user, site.ID, r.PathValue("record")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/dns", http.StatusSeeOther)
}

func humanBytes(value int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	number := float64(value)
	unit := 0
	for number >= 1024 && unit < len(units)-1 {
		number /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", number, units[unit])
}

func jobLabel(kind string) string {
	labels := map[string]string{
		"site.provision":              "Create site",
		"site.disable":                "Disable site",
		"site.enable":                 "Enable site",
		"site.config_apply":           "Apply expert configuration",
		"site.certificate":            "Issue certificate",
		"site.certificate_dns":        "Issue DNS certificate",
		"site.backup":                 "Create backup",
		"site.restore":                "Restore backup",
		"site.restore_clone":          "Restore as new site",
		"site.restore_test":           "Test backup restore",
		"wordpress.update":            "Update WordPress",
		"wordpress.performance_apply": "Apply WordPress performance settings",
		"wordpress.staging_create":    "Create staging",
		"wordpress.staging_sync":      "Sync staging",
		"wordpress.staging_deploy":    "Deploy staging",
		"dns.provider_verify":         "Verify DNS provider",
		"dns.record_apply":            "Apply DNS record",
		"dns.record_delete":           "Delete DNS record",
	}
	if label := labels[kind]; label != "" {
		return label
	}
	return strings.ReplaceAll(kind, ".", " · ")
}

func userAssignedSite(user store.User, siteID string) bool {
	for _, assigned := range user.SiteIDs {
		if assigned == siteID {
			return true
		}
	}
	return false
}

func parentDirectory(directory string) string {
	if directory == "" || directory == "." {
		return ""
	}
	parent := pathpkg.Dir(directory)
	if parent == "." {
		return ""
	}
	return parent
}

func (s *Server) setWordPressPlugin(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	action := r.FormValue("action")
	if action != "activate" && action != "deactivate" {
		http.Error(w, "invalid plugin action", http.StatusBadRequest)
		return
	}
	request := broker.WordPressPluginSetRequest{Site: site, Plugin: r.PathValue("plugin"), Active: action == "activate"}
	if s.broker == nil || s.broker.Call(r.Context(), broker.OpWordPressPluginSet, "wordpress.plugin:"+site.ID+":"+mustRandomHex(12), request, nil) != nil {
		http.Error(w, "could not change the WordPress plugin", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID, http.StatusSeeOther)
}

func (s *Server) setWordPressPerformance(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if _, err := s.store.SetWordPressPerformance(r.Context(), user, site.ID, r.FormValue("redis") == "on", r.FormValue("fastcgi_cache") == "on"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID, http.StatusSeeOther)
}

func (s *Server) updateWordPress(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	update := model.WordPressUpdate{Component: model.WordPressComponent(r.FormValue("component")), Name: r.FormValue("name")}
	if _, err := s.store.EnqueueWordPressUpdate(r.Context(), user, site.ID, r.FormValue("target_id"), update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"?update=queued", http.StatusSeeOther)
}

func (s *Server) wordpressHealthPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ViewSite)
	if !ok {
		return
	}
	if site.Kind != model.WordPress || site.Status != "active" {
		http.Error(w, "health checks require an active WordPress site", http.StatusBadRequest)
		return
	}
	if s.broker == nil {
		http.Error(w, "WordPress health checks are unavailable", http.StatusServiceUnavailable)
		return
	}
	var result broker.WordPressHealthResult
	if err := s.broker.Call(r.Context(), broker.OpWordPressHealth, "wordpress.health:"+site.ID+":"+mustRandomHex(8), broker.WordPressPluginsRequest{Site: site}, &result); err != nil {
		http.Error(w, "could not run WordPress health checks", http.StatusBadGateway)
		return
	}
	s.render(w, "wordpress_health.html", pageData{Title: "Health · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, WordPressHealth: &result})
}

func (s *Server) issueCertificate(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageTLS) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	providerID := strings.TrimSpace(r.FormValue("provider_id"))
	var enqueueErr error
	if providerID == "" {
		_, enqueueErr = s.store.EnqueueCertificate(r.Context(), user, site.ID)
	} else {
		_, enqueueErr = s.store.EnqueueDNSCertificate(r.Context(), user, site.ID, providerID, r.FormValue("wildcard") == "yes")
	}
	if enqueueErr != nil {
		providers, _ := s.store.ListDNSProviders(r.Context())
		s.renderStatus(w, "site.html", http.StatusBadRequest, pageData{Title: site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanWordPressLogin: site.Kind == model.WordPress && s.store.UserCanSite(r.Context(), user, site.ID, rbac.WordPressLogin), CanManageTLS: true, DNSProviders: providers, Error: enqueueErr.Error()})
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID, http.StatusSeeOther)
}

func (s *Server) wordpressLogin(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.WordPressLogin) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if s.broker == nil {
		http.Error(w, "WordPress operations are unavailable", http.StatusServiceUnavailable)
		return
	}
	var result broker.WordPressLoginResult
	requestID := "wordpress.login:" + site.ID + ":" + mustRandomHex(12)
	if err := s.broker.Call(r.Context(), broker.OpWordPressLogin, requestID, broker.WordPressLoginRequest{Site: site}, &result); err != nil {
		s.renderStatus(w, "site.html", http.StatusBadGateway, pageData{Title: site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanWordPressLogin: true, Error: err.Error()})
		return
	}
	http.Redirect(w, r, result.URL, http.StatusSeeOther)
}

func mustRandomHex(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic("cryptographic random source unavailable")
	}
	return hex.EncodeToString(b)
}

func (s *Server) securityPage(w http.ResponseWriter, r *http.Request, user store.User) {
	s.render(w, "security.html", pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers)})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.Authenticate(r.Context(), user.Username, r.FormValue("current_password")); err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), Error: "The current password is incorrect."})
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), Error: "The new passwords do not match."})
		return
	}
	if err := s.store.ChangePassword(r.Context(), user, password); err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), Error: err.Error()})
		return
	}
	s.clearLoginChallenge(w)
	http.SetCookie(w, &http.Cookie{Name: "wpx_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) beginTOTP(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	enrollment, err := s.store.BeginTOTPEnrollment(r.Context(), user)
	if err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), Error: err.Error()})
		return
	}
	s.render(w, "security.html", pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), TOTPSecret: enrollment.Secret, TOTPUri: enrollment.URI})
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	codes, err := s.store.ConfirmTOTPEnrollment(r.Context(), user.ID, r.FormValue("code"))
	if err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), Error: err.Error()})
		return
	}
	user.TOTPEnabled = true
	s.render(w, "security.html", pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), Message: "Two-factor authentication is enabled. Save these recovery codes now; they will not be shown again.", RecoveryCodes: codes})
}

func (s *Server) disableTOTP(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.Authenticate(r.Context(), user.Username, r.FormValue("password")); err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), Error: "The password is incorrect."})
		return
	}
	if err := s.store.DisableTOTP(r.Context(), user.ID); err != nil {
		s.renderStatus(w, "security.html", http.StatusBadRequest, pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), Error: err.Error()})
		return
	}
	user.TOTPEnabled = false
	s.render(w, "security.html", pageData{Title: "Account security", User: &user, CSRF: s.ensureCSRF(w, r), Message: "Two-factor authentication is disabled and its recovery codes were invalidated."})
}

func (s *Server) usersPage(w http.ResponseWriter, r *http.Request, user store.User) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "could not load users", http.StatusInternalServerError)
		return
	}
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	s.render(w, "users.html", pageData{Title: "Users", User: &user, CSRF: s.ensureCSRF(w, r), Users: users, Sites: sites, CanManageUsers: true})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, actor store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("password_confirm") {
		s.renderUsersError(w, r, actor, "The passwords do not match.")
		return
	}
	_, err := s.store.CreateUser(r.Context(), actor, r.FormValue("username"), password, rbac.Role(r.FormValue("role")), r.Form["site_ids"])
	if err != nil {
		s.renderUsersError(w, r, actor, err.Error())
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) editUserPage(w http.ResponseWriter, r *http.Request, actor store.User) {
	target, err := s.store.User(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if target.Role == rbac.Owner || target.ID == actor.ID || (actor.Role == rbac.Administrator && target.Role == rbac.Administrator) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	s.render(w, "user_edit.html", pageData{Title: "Edit " + target.Username, User: &actor, CSRF: s.ensureCSRF(w, r), Sites: sites, SelectedUser: &target, CanManageUsers: true})
}

func (s *Server) updateUserAccess(w http.ResponseWriter, r *http.Request, actor store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if err := s.store.UpdateUserAccess(r.Context(), actor, r.PathValue("id"), rbac.Role(r.FormValue("role")), r.Form["site_ids"]); err != nil {
		target, _ := s.store.User(r.Context(), r.PathValue("id"))
		sites, _ := s.store.ListSites(r.Context())
		s.renderStatus(w, "user_edit.html", http.StatusBadRequest, pageData{Title: "Edit user", User: &actor, CSRF: s.ensureCSRF(w, r), Sites: sites, SelectedUser: &target, CanManageUsers: true, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) setUserStatus(w http.ResponseWriter, r *http.Request, actor store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	action := r.FormValue("action")
	if action != "enable" && action != "disable" {
		s.renderUsersError(w, r, actor, "Choose enable or disable.")
		return
	}
	if err := s.store.SetUserDisabled(r.Context(), actor, r.PathValue("id"), action == "disable"); err != nil {
		s.renderUsersError(w, r, actor, err.Error())
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) renderUsersError(w http.ResponseWriter, r *http.Request, actor store.User, message string) {
	users, _ := s.store.ListUsers(r.Context())
	sites, _ := s.store.ListSites(r.Context())
	s.renderStatus(w, "users.html", http.StatusBadRequest, pageData{Title: "Users", User: &actor, CSRF: s.ensureCSRF(w, r), Users: users, Sites: sites, CanManageUsers: true, Error: message})
}

func (s *Server) renderSiteError(w http.ResponseWriter, r *http.Request, user store.User, message string) {
	sites, _ := s.store.ListSitesForUser(r.Context(), user)
	s.renderStatus(w, "sites.html", http.StatusBadRequest, pageData{Title: "Sites", User: &user, CSRF: s.ensureCSRF(w, r), Sites: sites, CanManageSites: rbac.Allows(user.Role, rbac.ManageAllSites), CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), Error: message})
}

type userHandler func(http.ResponseWriter, *http.Request, store.User)

func (s *Server) requireSession(next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("wpx_session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		user, err := s.store.ResolveSession(r.Context(), cookie.Value)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r, user)
	}
}

func (s *Server) requireCapability(capability rbac.Capability, next userHandler) userHandler {
	return func(w http.ResponseWriter, r *http.Request, user store.User) {
		if !rbac.Allows(user.Role, capability) {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		next(w, r, user)
	}
}

func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie("wpx_csrf"); err == nil && len(cookie.Value) == 64 {
		return cookie.Value
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("cryptographic random source unavailable")
	}
	token := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: "wpx_csrf", Value: token, Path: "/", MaxAge: 86400, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return token
}

func (s *Server) validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("wpx_csrf")
	if err != nil || len(cookie.Value) != 64 {
		return false
	}
	provided := r.FormValue("csrf_token")
	if provided == "" {
		provided = r.Header.Get("X-CSRF-Token")
	}
	return len(provided) == len(cookie.Value) && subtle.ConstantTimeCompare([]byte(provided), []byte(cookie.Value)) == 1
}

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: "wpx_session", Value: token, Path: "/", MaxAge: 1800, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	s.renderStatus(w, name, http.StatusOK, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, name string, status int, data pageData) {
	if data.User != nil {
		data.CanManageUsers = rbac.Allows(data.User.Role, rbac.ManageUsers)
		data.CanManageServer = rbac.Allows(data.User.Role, rbac.ManageServer)
		data.CanManageSites = rbac.Allows(data.User.Role, rbac.ManageAllSites)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("render template", "template", name, "error", err)
	}
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("web panic", "value", recovered)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
