package install

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/config"
)

func TestWaitForPanelRequiresLocalHealthInsteadOfFollowingRedirects(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					t.Errorf("unexpected health request %s", r.URL.Path)
				}
				if status == http.StatusFound {
					w.Header().Set("Location", "/login")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			cfg := config.Default()
			cfg.ListenAddress = strings.TrimPrefix(server.URL, "https://")
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := WaitForPanel(ctx, cfg)
			if status == http.StatusOK && err != nil {
				t.Fatal(err)
			}
			if status != http.StatusOK && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unhealthy endpoint must time out, got %v", err)
			}
		})
	}
}
