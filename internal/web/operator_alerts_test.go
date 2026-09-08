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

func TestOperatorAlertsUsesProgressiveDeliveryAndGranularRows(t *testing.T) {
	server, owner, _ := navigationServer(t)
	response := navigationRequest(t, server, owner, http.MethodGet, "/alerts", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	for _, expected := range []string{
		`src="/assets/alerts.js?v=`, `name="channels_version" value="1"`, `data-alert-master`,
		`data-alert-channel="smtp"`, `data-alert-channel="slack"`, `data-alert-channel="telegram"`,
		`name="smtp_enabled"`, `name="slack_enabled"`, `name="slack_webhook_url"`,
		`name="telegram_enabled"`, `name="telegram_bot_token"`, `name="telegram_chat_id"`,
		`>Email delivery</h2>`, `>Notifications</h2>`, `>Stability</span>`,
		`name="cpu_percent"`, `name="memory_percent"`, `name="disk_percent"`,
		`name="ssl_expiry_days"`, `name="cooldown_minutes"`, `name="auto_swap"`,
		`name="test_channel" value="smtp">Send email test</button>`,
		`name="test_channel" value="slack">Send Slack test</button>`,
		`name="test_channel" value="telegram">Send Telegram test</button>`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("alerts page missing %q", expected)
		}
	}
	if count := strings.Count(body, `data-channel-fields hidden`); count != 3 {
		t.Errorf("collapsed channel panels = %d, want 3", count)
	}
	if count := strings.Count(body, "data-channel-required"); count != 7 || strings.Contains(body, "data-channel-required required") {
		t.Errorf("collapsed required controls are unsafe: markers=%d", count)
	}
	if switches := strings.Count(body, `role="switch"`); switches != 12 {
		t.Errorf("switch count = %d, want 12", switches)
	}
	if !strings.Contains(body, `min="15" max="10080" name="cooldown_minutes"`) || !strings.Contains(body, `Requires at least 3 GiB`) {
		t.Error("stability limits are not explicit")
	}
}

func TestOperatorAlertDisableKeepsSMTPValuesWithoutHiddenRequiredControls(t *testing.T) {
	server, owner, _ := navigationServer(t)
	form := url.Values{"enabled": {"yes"}, "cpu_percent": {"90"}, "memory_percent": {"90"}, "disk_percent": {"90"}, "ssl_expiry_days": {"14"}, "cooldown_minutes": {"60"}, "host": {"smtp.example.com"}, "port": {"587"}, "transport": {"starttls"}, "password": {"stored-secret"}, "from": {"alerts@example.com"}, "to": {"ops@example.com"}}
	requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodPost, "/alerts", form), http.StatusSeeOther)
	enabledPage := navigationRequest(t, server, owner, http.MethodGet, "/alerts", nil)
	requireNavigationStatus(t, enabledPage, http.StatusOK)
	if strings.Contains(enabledPage.Body.String(), "data-channel-required required") {
		t.Fatal("collapsed delivery rendered required controls")
	}
	form.Del("enabled")
	form.Set("password", "")
	requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodPost, "/alerts", form), http.StatusSeeOther)
	settings, err := server.store.OperatorAlertSettings(context.Background())
	if err != nil || settings.Enabled || settings.SMTP.Host != "smtp.example.com" || settings.SMTP.Password != "stored-secret" {
		t.Fatalf("disabled settings lost delivery state: %#v err=%v", settings, err)
	}
	response := navigationRequest(t, server, owner, http.MethodGet, "/alerts", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	if !strings.Contains(body, `lg:grid-cols-3" data-channel-fields hidden`) || strings.Contains(body, "data-channel-required required") {
		t.Fatal("disabled delivery renders an invalid hidden required control")
	}

}

func TestOperatorAlertChannelsPreserveAndRedactSecrets(t *testing.T) {
	server, owner, _ := navigationServer(t)
	form := url.Values{
		"channels_version": {"1"}, "enabled": {"yes"}, "smtp_enabled": {"yes"}, "slack_enabled": {"yes"}, "telegram_enabled": {"yes"},
		"cpu_percent": {"90"}, "memory_percent": {"90"}, "disk_percent": {"90"}, "ssl_expiry_days": {"14"}, "cooldown_minutes": {"60"},
		"host": {"smtp.example.com"}, "port": {"587"}, "transport": {"starttls"}, "password": {"smtp-secret"}, "from": {"alerts@example.com"}, "to": {"ops@example.com"},
		"slack_webhook_url":  {"https://hooks.slack.com/services/T123/B456/secret"},
		"telegram_bot_token": {"123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"}, "telegram_chat_id": {"-100123456789"},
	}
	requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodPost, "/alerts", form), http.StatusSeeOther)
	form.Set("password", "")
	form.Set("slack_webhook_url", "")
	form.Set("telegram_bot_token", "")
	requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodPost, "/alerts", form), http.StatusSeeOther)
	settings, err := server.store.OperatorAlertSettings(context.Background())
	if err != nil || settings.SMTP.Password != "smtp-secret" || settings.Slack.WebhookURL != "https://hooks.slack.com/services/T123/B456/secret" || settings.Telegram.BotToken != "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef" {
		t.Fatalf("channel secrets were not preserved: settings=%#v err=%v", settings, err)
	}
	response := navigationRequest(t, server, owner, http.MethodGet, "/alerts", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	for _, secret := range []string{"smtp-secret", "https://hooks.slack.com/services/T123/B456/secret", "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"} {
		if strings.Contains(body, secret) {
			t.Fatalf("rendered alert channel secret %q", secret)
		}
	}
	if !strings.Contains(body, `value="-100123456789"`) || strings.Count(body, ">Configured</span>") < 2 {
		t.Fatal("channel configuration status was not rendered")
	}
}

func TestOperatorAlertChannelFormAllowsPausedBlankDelivery(t *testing.T) {
	server, owner, _ := navigationServer(t)
	form := url.Values{"channels_version": {"1"}, "cpu_percent": {"90"}, "memory_percent": {"90"}, "disk_percent": {"90"}, "ssl_expiry_days": {"14"}, "cooldown_minutes": {"60"}}
	requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodPost, "/alerts", form), http.StatusSeeOther)
	settings, err := server.store.OperatorAlertSettings(context.Background())
	if err != nil || settings.Enabled || settings.SMTPEnabled || settings.Slack.Enabled || settings.Telegram.Enabled {
		t.Fatalf("paused blank channels were not saved: settings=%#v err=%v", settings, err)
	}
}

func TestOperatorAlertTestValidatesOnlySelectedChannel(t *testing.T) {
	server, owner, _ := navigationServer(t)
	form := url.Values{
		"channels_version": {"1"}, "enabled": {"yes"}, "smtp_enabled": {"yes"}, "slack_enabled": {"yes"}, "test_channel": {"smtp"},
		"cpu_percent": {"90"}, "memory_percent": {"90"}, "disk_percent": {"90"}, "ssl_expiry_days": {"14"}, "cooldown_minutes": {"60"},
		"host": {"smtp.example.com"}, "port": {"587"}, "transport": {"starttls"}, "password": {"smtp-secret"}, "from": {"alerts@example.com"}, "to": {"ops@example.com"},
	}
	response := navigationRequest(t, server, owner, http.MethodPost, "/alerts/test", form)
	requireNavigationStatus(t, response, http.StatusUnprocessableEntity)
	if !strings.Contains(response.Body.String(), "SMTP test delivery failed") || strings.Contains(response.Body.String(), "Slack webhook URL is invalid") {
		t.Fatal("selected SMTP test was coupled to incomplete Slack settings")
	}
	form.Set("test_channel", "all")
	response = navigationRequest(t, server, owner, http.MethodPost, "/alerts/test", form)
	requireNavigationStatus(t, response, http.StatusUnprocessableEntity)
}
