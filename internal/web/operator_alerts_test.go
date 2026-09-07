//go:build linux

package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/rbac"
)

func TestOperatorAlertRoutesAuthorizeAndNeverRenderSMTPPassword(t *testing.T) {
	server, owner, _ := navigationServer(t)
	response := navigationRequest(t, server, owner, http.MethodGet, "/alerts", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	form := url.Values{"enabled": {"yes"}, "cpu": {"yes"}, "memory": {"yes"}, "disk": {"yes"}, "services": {"yes"}, "ssl_expiry": {"yes"}, "updates": {"yes"}, "oom": {"yes"}, "cpu_percent": {"90"}, "memory_percent": {"90"}, "disk_percent": {"90"}, "ssl_expiry_days": {"14"}, "cooldown_minutes": {"60"}, "host": {"smtp.example.com"}, "port": {"587"}, "transport": {"starttls"}, "username": {"operator"}, "password": {"mail-secret-never-rendered"}, "from": {"alerts@example.com"}, "to": {"ops@example.com"}}
	response = navigationRequest(t, server, owner, http.MethodPost, "/alerts", form)
	requireNavigationStatus(t, response, http.StatusSeeOther)
	response = navigationRequest(t, server, owner, http.MethodGet, "/alerts", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	if strings.Contains(response.Body.String(), "mail-secret-never-rendered") {
		t.Fatal("SMTP password rendered")
	}
	customer, err := server.store.CreateUser(context.Background(), owner, "customer-alerts", "a-secure-test-password", rbac.Customer, nil)
	if err != nil {
		t.Fatal(err)
	}
	response = navigationRequest(t, server, customer, http.MethodGet, "/alerts", nil)
	requireNavigationStatus(t, response, http.StatusForbidden)
	response = navigationRequest(t, server, customer, http.MethodPost, "/alerts", form)
	requireNavigationStatus(t, response, http.StatusForbidden)
}

func TestOperatorAlertSaveKeepsExistingPasswordWhenBlank(t *testing.T) {
	server, owner, _ := navigationServer(t)
	base := url.Values{"cpu_percent": {"90"}, "memory_percent": {"90"}, "disk_percent": {"90"}, "ssl_expiry_days": {"14"}, "cooldown_minutes": {"60"}, "host": {"smtp.example.com"}, "port": {"465"}, "transport": {"tls"}, "username": {"operator"}, "password": {"stored-secret"}, "from": {"alerts@example.com"}, "to": {"ops@example.com"}}
	requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodPost, "/alerts", base), http.StatusSeeOther)
	base.Set("password", "")
	base.Set("to", "oncall@example.com")
	requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodPost, "/alerts", base), http.StatusSeeOther)
	settings, err := server.store.OperatorAlertSettings(context.Background())
	if err != nil || settings.SMTP.Password != "stored-secret" || settings.SMTP.To != "oncall@example.com" {
		t.Fatalf("settings=%#v err=%v", settings, err)
	}
}
