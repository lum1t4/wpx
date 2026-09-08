package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

type operatorAlertsView struct {
	Settings               model.OperatorAlertSettings
	SMTPRules              operatorAlertChannelRulesView
	SlackRules             operatorAlertChannelRulesView
	TelegramRules          operatorAlertChannelRulesView
	PasswordConfigured     bool
	SlackWebhookConfigured bool
	TelegramBotConfigured  bool
}

type operatorAlertChannelRulesView struct {
	Prefix    string
	Selection model.AlertRuleSelection
}

func (s *Server) registerOperatorAlertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /alerts", s.requireSession(s.requireCapability(rbac.ManageServer, s.operatorAlertsPage)))
	mux.HandleFunc("POST /alerts", s.requireSession(s.requireCapability(rbac.ManageServer, s.saveOperatorAlerts)))
	mux.HandleFunc("POST /alerts/toggle", s.requireSession(s.requireCapability(rbac.ManageServer, s.toggleOperatorAlerts)))
	mux.HandleFunc("POST /alerts/test", s.requireSession(s.requireCapability(rbac.ManageServer, s.testOperatorAlerts)))
}

func (s *Server) toggleOperatorAlerts(w http.ResponseWriter, r *http.Request, user store.User) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		writeOperatorAlertToggleError(w, "invalid or oversized request", http.StatusBadRequest)
		return
	}
	if !s.validCSRF(r) {
		writeOperatorAlertToggleError(w, "invalid form token", http.StatusForbidden)
		return
	}
	settings, err := s.store.OperatorAlertSettings(r.Context())
	if err != nil {
		writeOperatorAlertToggleError(w, "alert settings unavailable", http.StatusServiceUnavailable)
		return
	}
	value := r.FormValue("value")
	if value != "yes" && value != "no" {
		writeOperatorAlertToggleError(w, "invalid alert setting", http.StatusBadRequest)
		return
	}
	on := value == "yes"
	name := r.FormValue("name")
	switch name {
	case "enabled":
		settings.Enabled = on
	case "smtp_enabled":
		settings.SMTPEnabled = on
	case "slack_enabled":
		settings.Slack.Enabled = on
	case "telegram_enabled":
		settings.Telegram.Enabled = on
	case "auto_swap":
		settings.AutoSwap = on
	default:
		writeOperatorAlertToggleError(w, "invalid alert setting", http.StatusBadRequest)
		return
	}
	if name != "enabled" && !on && settings.Enabled && !settings.SMTPEnabled && !settings.Slack.Enabled && !settings.Telegram.Enabled {
		settings.Enabled = false
	}
	if err := s.store.SaveOperatorAlertSettings(r.Context(), user, settings); err != nil {
		writeOperatorAlertToggleError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "enabled": settings.Enabled, "smtp_enabled": settings.SMTPEnabled,
		"slack_enabled": settings.Slack.Enabled, "telegram_enabled": settings.Telegram.Enabled, "auto_swap": settings.AutoSwap,
	})
}

func writeOperatorAlertToggleError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
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
		s.renderStatus(w, "alerts.html", http.StatusUnprocessableEntity, pageData{Title: "Alerts", User: &user, CSRF: s.ensureCSRF(w, r), OperatorAlerts: operatorAlertView(settings), Form: map[string]string{"unsaved": "yes"}, Error: err.Error()})
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
	channel := strings.ToLower(strings.TrimSpace(r.FormValue("test_channel")))
	if err == nil {
		if channel != "smtp" && channel != "slack" && channel != "telegram" {
			err = errors.New("choose an alert channel to test")
		} else {
			err = s.alerts.TestChannel(r.Context(), settings, channel)
		}
	}
	view := pageData{Title: "Alerts", User: &user, CSRF: s.ensureCSRF(w, r), OperatorAlerts: operatorAlertView(settings), Form: map[string]string{"unsaved": "yes"}}
	if err != nil {
		view.Error = err.Error()
		s.renderStatus(w, "alerts.html", http.StatusUnprocessableEntity, view)
		return
	}
	labels := map[string]string{"smtp": "email", "slack": "Slack", "telegram": "Telegram"}
	view.Message = "Test alert sent to " + labels[channel] + "."
	s.render(w, "alerts.html", view)
}

func (s *Server) operatorAlertSettingsFromForm(r *http.Request) (model.OperatorAlertSettings, error) {
	current, _ := s.store.OperatorAlertSettings(r.Context())
	integer := func(name string) int { value, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(name))); return value }
	password := r.FormValue("password")
	if password == "" {
		password = current.SMTP.Password
	}
	slackWebhook := strings.TrimSpace(r.FormValue("slack_webhook_url"))
	if slackWebhook == "" {
		slackWebhook = current.Slack.WebhookURL
	}
	telegramToken := strings.TrimSpace(r.FormValue("telegram_bot_token"))
	if telegramToken == "" {
		telegramToken = current.Telegram.BotToken
	}
	smtpEnabled := true
	if r.FormValue("channels_version") == "1" {
		smtpEnabled = checked(r, "smtp_enabled")
	}
	settings := model.OperatorAlertSettings{
		Enabled: checked(r, "enabled"), CPU: checked(r, "cpu"), Memory: checked(r, "memory"), Disk: checked(r, "disk"), Services: checked(r, "services"), SSLExpiry: checked(r, "ssl_expiry"), Updates: checked(r, "updates"), OOM: checked(r, "oom"), AutoSwap: checked(r, "auto_swap"),
		CPUPercent: integer("cpu_percent"), MemoryPercent: integer("memory_percent"), DiskPercent: integer("disk_percent"), SSLExpiryDays: integer("ssl_expiry_days"), CooldownMins: integer("cooldown_minutes"),
		SMTPEnabled: smtpEnabled,
		SMTP:        model.SMTPConfig{Host: strings.TrimSpace(r.FormValue("host")), Port: integer("port"), Transport: model.SMTPTransport(r.FormValue("transport")), Username: strings.TrimSpace(r.FormValue("username")), Password: password, From: strings.TrimSpace(r.FormValue("from")), To: strings.TrimSpace(r.FormValue("to"))},
		Slack:       model.SlackAlertConfig{Enabled: checked(r, "slack_enabled"), WebhookURL: slackWebhook},
		Telegram:    model.TelegramAlertConfig{Enabled: checked(r, "telegram_enabled"), BotToken: telegramToken, ChatID: strings.TrimSpace(r.FormValue("telegram_chat_id"))},
	}
	if r.FormValue("rules_version") == "1" {
		settings.SMTPRules = alertRuleSelectionFromForm(r, "smtp")
		settings.Slack.Rules = alertRuleSelectionFromForm(r, "slack")
		settings.Telegram.Rules = alertRuleSelectionFromForm(r, "telegram")
	} else {
		settings.SMTPRules = current.SMTPRules
		settings.Slack.Rules = current.Slack.Rules
		settings.Telegram.Rules = current.Telegram.Rules
	}
	return settings, nil
}

func alertRuleSelectionFromForm(r *http.Request, prefix string) *model.AlertRuleSelection {
	return &model.AlertRuleSelection{
		CPU: checked(r, prefix+"_rule_cpu"), Memory: checked(r, prefix+"_rule_memory"), Disk: checked(r, prefix+"_rule_disk"),
		Services: checked(r, prefix+"_rule_services"), SSLExpiry: checked(r, prefix+"_rule_ssl_expiry"),
		Updates: checked(r, prefix+"_rule_updates"), OOM: checked(r, prefix+"_rule_oom"),
	}
}

func operatorAlertView(settings model.OperatorAlertSettings) *operatorAlertsView {
	return &operatorAlertsView{
		Settings:               model.PublicOperatorAlertSettings(settings),
		SMTPRules:              operatorAlertChannelRulesView{Prefix: "smtp", Selection: settings.RulesForChannel("smtp")},
		SlackRules:             operatorAlertChannelRulesView{Prefix: "slack", Selection: settings.RulesForChannel("slack")},
		TelegramRules:          operatorAlertChannelRulesView{Prefix: "telegram", Selection: settings.RulesForChannel("telegram")},
		PasswordConfigured:     settings.SMTP.Password != "",
		SlackWebhookConfigured: settings.Slack.WebhookURL != "",
		TelegramBotConfigured:  settings.Telegram.BotToken != "",
	}
}

func checked(r *http.Request, name string) bool { return r.FormValue(name) == "yes" }
