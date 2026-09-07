package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

type operatorAlertsView struct {
	Settings           model.OperatorAlertSettings
	PasswordConfigured bool
}

func (s *Server) registerOperatorAlertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /alerts", s.requireSession(s.requireCapability(rbac.ManageServer, s.operatorAlertsPage)))
	mux.HandleFunc("POST /alerts", s.requireSession(s.requireCapability(rbac.ManageServer, s.saveOperatorAlerts)))
	mux.HandleFunc("POST /alerts/test", s.requireSession(s.requireCapability(rbac.ManageServer, s.testOperatorAlerts)))
}

func (s *Server) MonitorOperatorAlerts(ctx context.Context) { s.alerts.Run(ctx) }

func (s *Server) operatorAlertsPage(w http.ResponseWriter, r *http.Request, user store.User) {
	settings, err := s.store.OperatorAlertSettings(r.Context())
	if err != nil {
		http.Error(w, "alert settings unavailable", http.StatusInternalServerError)
		return
	}
	message := ""
	if r.URL.Query().Get("saved") == "1" {
		message = "Alert settings saved."
	}
	s.render(w, "alerts.html", pageData{Title: "Alerts", User: &user, CSRF: s.ensureCSRF(w, r), OperatorAlerts: operatorAlertView(settings), Message: message})
}

func (s *Server) saveOperatorAlerts(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return
	}
	settings, err := s.operatorAlertSettingsFromForm(r)
	if err == nil {
		err = s.store.SaveOperatorAlertSettings(r.Context(), user, settings)
	}
	if err != nil {
		s.renderStatus(w, "alerts.html", http.StatusUnprocessableEntity, pageData{Title: "Alerts", User: &user, CSRF: s.ensureCSRF(w, r), OperatorAlerts: operatorAlertView(settings), Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/alerts?saved=1", http.StatusSeeOther)
}

func (s *Server) testOperatorAlerts(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return
	}
	settings, err := s.operatorAlertSettingsFromForm(r)
	if err == nil {
		err = s.alerts.Test(r.Context(), settings)
	}
	view := pageData{Title: "Alerts", User: &user, CSRF: s.ensureCSRF(w, r), OperatorAlerts: operatorAlertView(settings)}
	if err != nil {
		view.Error = err.Error()
		s.renderStatus(w, "alerts.html", http.StatusUnprocessableEntity, view)
		return
	}
	view.Message = "Test alert sent."
	s.render(w, "alerts.html", view)
}

func (s *Server) operatorAlertSettingsFromForm(r *http.Request) (model.OperatorAlertSettings, error) {
	current, _ := s.store.OperatorAlertSettings(r.Context())
	integer := func(name string) int { value, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(name))); return value }
	password := r.FormValue("password")
	if password == "" {
		password = current.SMTP.Password
	}
	settings := model.OperatorAlertSettings{
		Enabled: checked(r, "enabled"), CPU: checked(r, "cpu"), Memory: checked(r, "memory"), Disk: checked(r, "disk"), Services: checked(r, "services"), SSLExpiry: checked(r, "ssl_expiry"), Updates: checked(r, "updates"), OOM: checked(r, "oom"), AutoSwap: checked(r, "auto_swap"),
		CPUPercent: integer("cpu_percent"), MemoryPercent: integer("memory_percent"), DiskPercent: integer("disk_percent"), SSLExpiryDays: integer("ssl_expiry_days"), CooldownMins: integer("cooldown_minutes"),
		SMTP: model.SMTPConfig{Host: strings.TrimSpace(r.FormValue("host")), Port: integer("port"), Transport: model.SMTPTransport(r.FormValue("transport")), Username: strings.TrimSpace(r.FormValue("username")), Password: password, From: strings.TrimSpace(r.FormValue("from")), To: strings.TrimSpace(r.FormValue("to"))},
	}
	return settings, model.ValidateOperatorAlertSettings(settings)
}

func operatorAlertView(settings model.OperatorAlertSettings) *operatorAlertsView {
	password := settings.SMTP.Password != ""
	return &operatorAlertsView{Settings: model.PublicOperatorAlertSettings(settings), PasswordConfigured: password}
}

func checked(r *http.Request, name string) bool { return r.FormValue(name) == "yes" }
