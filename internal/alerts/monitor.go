package alerts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	resources "github.com/lum1t4/wpx/internal/monitor"
	"github.com/lum1t4/wpx/internal/store"
)

const CheckInterval = time.Minute

type resourceReader interface{ Snapshot() resources.Snapshot }
type brokerCaller interface {
	Call(context.Context, broker.Operation, string, any, any) error
}

type Monitor struct {
	store     *store.Store
	resources resourceReader
	broker    brokerCaller
	sender    Sender
	channels  ChannelSender
	logger    *slog.Logger
	now       func() time.Time
	interval  time.Duration
	mu        sync.Mutex
}

func New(state *store.Store, resourceMonitor resourceReader, privileged brokerCaller, logger *slog.Logger) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Monitor{store: state, resources: resourceMonitor, broker: privileged, sender: SMTPSender{}, channels: WebhookSender{}, logger: logger, now: time.Now, interval: CheckInterval}
}

func (m *Monitor) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	m.Evaluate(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Evaluate(ctx)
		}
	}
}

func (m *Monitor) Evaluate(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings, err := m.store.OperatorAlertSettings(ctx)
	if err != nil {
		m.logger.Error("load operator alert settings", "error", err)
		return
	}
	if !settings.Enabled {
		return
	}
	snapshot := m.resources.Snapshot()
	sites, err := m.store.ListSites(ctx)
	if err != nil {
		m.logger.Error("load sites for alerts", "error", err)
		return
	}
	var diagnostics broker.OperatorDiagnosticsResult
	var nodeRuntimes []model.NodeRuntime
	for _, site := range sites {
		if site.Kind != model.ReverseProxy {
			continue
		}
		runtime, runtimeErr := m.store.NodeRuntime(ctx, site.ID)
		if runtimeErr == nil {
			nodeRuntimes = append(nodeRuntimes, runtime)
		} else if !errors.Is(runtimeErr, sql.ErrNoRows) {
			m.logger.Warn("load Node runtime for alerts", "site", site.ID, "error", runtimeErr)
		}
	}
	if m.broker != nil {
		err = m.broker.Call(ctx, broker.OpOperatorDiagnostics, "operator-alert-diagnostics", broker.OperatorDiagnosticsRequest{Sites: sites, NodeRuntimes: nodeRuntimes}, &diagnostics)
		if err != nil {
			m.logger.Warn("collect operator diagnostics", "error", err)
		}
	}
	if settings.AutoSwap && snapshot.Error == "" && snapshot.Current.MemoryTotal > 0 && snapshot.Current.SwapTotal == 0 && m.broker != nil {
		var result broker.OperatorEnsureSwapResult
		if err := m.broker.Call(ctx, broker.OpOperatorEnsureSwap, "operator-alert-managed-swap", broker.OperatorEnsureSwapRequest{SizeMiB: 2048}, &result); err != nil {
			m.logger.Error("ensure managed swap", "error", err)
		}
	}
	now := m.now().UTC()
	if settings.CPU && snapshot.Error == "" && snapshot.Current.CPUReady {
		m.condition(ctx, settings, "cpu", snapshot.Current.CPUPercent >= float64(settings.CPUPercent), 3, fmt.Sprintf("CPU usage is %.1f%% (threshold %d%%).", snapshot.Current.CPUPercent, settings.CPUPercent), resourceDetail(snapshot))
	}
	if settings.Memory && snapshot.Error == "" && snapshot.Current.MemoryReady {
		m.condition(ctx, settings, "memory", snapshot.Current.MemoryPercent >= float64(settings.MemoryPercent), 3, fmt.Sprintf("Memory usage is %.1f%% (threshold %d%%).", snapshot.Current.MemoryPercent, settings.MemoryPercent), resourceDetail(snapshot))
	}
	if settings.Disk && snapshot.Error == "" {
		for _, disk := range snapshot.Current.Disks {
			if disk.Error == "" && disk.Total > 0 {
				percent := 100 * float64(disk.Used) / float64(disk.Total)
				m.condition(ctx, settings, "disk:"+disk.Path, percent >= float64(settings.DiskPercent), 3, fmt.Sprintf("Disk usage on %s is %.1f%% (threshold %d%%).", disk.Path, percent, settings.DiskPercent), resourceDetail(snapshot))
			}
		}
	}
	if settings.Services {
		for _, service := range diagnostics.Services {
			unhealthy := service.Active == "failed" || (service.Required && service.Active != "active")
			detail := strings.Join(service.Diagnostics, "\n")
			if detail == "" {
				detail = resourceDetail(snapshot)
			}
			m.condition(ctx, settings, "service:"+service.Name, unhealthy, 1, fmt.Sprintf("Service %s is %s/%s (restarts %d, exit %d).", service.Name, service.Active, service.Sub, service.Restarts, service.ExitStatus), detail)
			if service.Restarts > 0 {
				event := fmt.Sprintf("Service %s has restarted %d times (current state %s/%s, exit %d).", service.Name, service.Restarts, service.Active, service.Sub, service.ExitStatus)
				m.event(ctx, settings, "service-crash:"+service.Name, "WPX alert: service restart", event, detail)
			}
		}
	}
	if settings.SSLExpiry {
		for _, certificate := range diagnostics.Certificates {
			bad, summary := certificate.Error != "", "Certificate for "+certificate.Domain+" cannot be inspected."
			if certificate.Error == "" {
				expiry, parseErr := time.Parse(time.RFC3339, certificate.NotAfter)
				if parseErr != nil {
					bad = true
				} else {
					days := int(expiry.Sub(now).Hours() / 24)
					bad = expiry.Before(now.Add(time.Duration(settings.SSLExpiryDays) * 24 * time.Hour))
					summary = fmt.Sprintf("Certificate for %s expires in %d days (%s).", certificate.Domain, days, expiry.Format("2006-01-02"))
				}
			}
			m.condition(ctx, settings, "ssl:"+certificate.SiteID, bad, 1, summary, certificate.Error)
		}
	}
	if settings.Updates {
		if update, updateErr := m.store.UpdateStatus(ctx); updateErr == nil && update.Status == "ok" && update.LatestVersion != "" {
			m.condition(ctx, settings, "update", update.CurrentVersion != update.LatestVersion, 1, fmt.Sprintf("WPX %s is available; this server runs %s.\n%s", update.LatestVersion, update.CurrentVersion, update.ReleaseURL), "")
		}
	}
	if settings.OOM && len(diagnostics.OOMEvents) > 0 {
		m.event(ctx, settings, "oom", "WPX alert: out of memory event", strings.Join(diagnostics.OOMEvents, "\n"), resourceDetail(snapshot))
	}
}

func (m *Monitor) condition(ctx context.Context, settings model.OperatorAlertSettings, key string, bad bool, trigger int, summary, detail string) {
	state, err := m.store.OperatorAlertState(ctx, key)
	if err != nil {
		m.logger.Error("load alert state", "key", key, "error", err)
		return
	}
	now := m.now().UTC()
	if bad {
		state.GoodSamples = 0
		if state.BadSamples < trigger {
			state.BadSamples++
		}
		cooldown := time.Duration(settings.CooldownMins) * time.Minute
		if state.BadSamples >= trigger {
			if !state.Active && (state.LastSentAt.IsZero() || now.Sub(state.LastSentAt) >= cooldown) {
				state.LastFingerprint = "open:" + nextConditionCycle(state.LastFingerprint)
				state.Active = true
				state.LastSentAt = now
			}
			if state.Active {
				identity := state.LastFingerprint
				if !strings.HasPrefix(identity, "open:") {
					identity = "open:1"
					state.LastFingerprint = identity
				}
				_ = m.send(ctx, settings, key, identity, "WPX alert: "+key, summary, detail)
			}
		}
	} else {
		state.BadSamples = 0
		if state.Active {
			state.GoodSamples++
			if state.GoodSamples >= 2 {
				cycle := conditionCycle(state.LastFingerprint)
				state.Active = false
				state.GoodSamples = 0
				state.LastSentAt = now
				state.LastFingerprint = "resolved:" + cycle
				_ = m.send(ctx, settings, key, state.LastFingerprint, "WPX resolved: "+key, summary+" The condition has recovered.", detail)
			}
		} else {
			state.GoodSamples = 0
			if strings.HasPrefix(state.LastFingerprint, "resolved:") {
				_ = m.send(ctx, settings, key, state.LastFingerprint, "WPX resolved: "+key, summary+" The condition has recovered.", detail)
			}
		}
	}
	if err := m.store.SaveOperatorAlertState(ctx, state); err != nil {
		m.logger.Error("save alert state", "key", key, "error", err)
	}
}

func conditionCycle(phase string) string {
	_, cycle, found := strings.Cut(phase, ":")
	if !found {
		return "1"
	}
	if _, err := strconv.ParseUint(cycle, 10, 64); err != nil {
		return "1"
	}
	return cycle
}

func nextConditionCycle(phase string) string {
	cycle, _ := strconv.ParseUint(conditionCycle(phase), 10, 64)
	if phase == "" {
		return "1"
	}
	return strconv.FormatUint(cycle+1, 10)
}

func (m *Monitor) event(ctx context.Context, settings model.OperatorAlertSettings, key, subject, event, detail string) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(event)))
	state, err := m.store.OperatorAlertState(ctx, key)
	if err != nil {
		return
	}
	now := m.now().UTC()
	cooldown := time.Duration(settings.CooldownMins) * time.Minute
	if digest != state.LastFingerprint && (state.LastSentAt.IsZero() || now.Sub(state.LastSentAt) >= cooldown) {
		if m.send(ctx, settings, key, digest, subject, event, detail) == nil {
			state.LastSentAt = now
			state.LastFingerprint = digest
		}
	}
	if err := m.store.SaveOperatorAlertState(ctx, state); err != nil {
		m.logger.Error("save alert event state", "key", key, "error", err)
	}
}

func (m *Monitor) send(ctx context.Context, settings model.OperatorAlertSettings, incidentKey, deliveryIdentity, subject, summary, detail string) error {
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	if len(subject) > 160 {
		subject = subject[:160]
	}
	body := summary
	if strings.TrimSpace(detail) != "" {
		body += "\n\nBounded diagnostic snapshot:\n" + bounded(detail, 8192)
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(deliveryIdentity)))
	type delivery struct {
		name string
		send func() error
	}
	var deliveries []delivery
	if settings.SMTPEnabled {
		deliveries = append(deliveries, delivery{"smtp", func() error { return m.sender.Send(ctx, settings.SMTP, subject, body) }})
	}
	if settings.Slack.Enabled {
		deliveries = append(deliveries, delivery{"slack", func() error { return m.channels.SendSlack(ctx, settings.Slack, subject, body) }})
	}
	if settings.Telegram.Enabled {
		deliveries = append(deliveries, delivery{"telegram", func() error { return m.channels.SendTelegram(ctx, settings.Telegram, subject, body) }})
	}
	if len(deliveries) == 0 {
		return errors.New("no alert delivery channel enabled")
	}
	cooldown := time.Duration(settings.CooldownMins) * time.Minute
	now := m.now().UTC()
	allDelivered := true
	incidentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(incidentKey)))[:24]
	for _, channel := range deliveries {
		stateKey := "delivery:" + incidentHash + ":" + channel.name
		state, err := m.store.OperatorAlertState(ctx, stateKey)
		if err != nil {
			m.logger.Error("load alert delivery state", "channel", channel.name, "error", err)
			allDelivered = false
			continue
		}
		if state.LastFingerprint == fingerprint && !state.Active {
			continue
		}
		if state.Active && !state.LastSentAt.IsZero() && now.Sub(state.LastSentAt) < cooldown {
			allDelivered = false
			continue
		}
		state.LastSentAt = now
		if err := channel.send(); err != nil {
			state.Active = true // failed attempt; retry after the channel cooldown
			allDelivered = false
			m.logger.Error("send operator alert", "channel", channel.name, "error", channel.name+" delivery failed")
		} else {
			state.Active = false
			state.LastFingerprint = fingerprint
		}
		if err := m.store.SaveOperatorAlertState(ctx, state); err != nil {
			m.logger.Error("save alert delivery state", "channel", channel.name, "error", err)
			allDelivered = false
		}
	}
	if !allDelivered {
		return errors.New("one or more alert channels were not delivered")
	}
	return nil
}

func (m *Monitor) Test(ctx context.Context, settings model.OperatorAlertSettings) error {
	var failures []error
	if settings.SMTPEnabled {
		if err := m.TestChannel(ctx, settings, "smtp"); err != nil {
			failures = append(failures, err)
		}
	}
	if settings.Slack.Enabled {
		if err := m.TestChannel(ctx, settings, "slack"); err != nil {
			failures = append(failures, err)
		}
	}
	if settings.Telegram.Enabled {
		if err := m.TestChannel(ctx, settings, "telegram"); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 && !settings.SMTPEnabled && !settings.Slack.Enabled && !settings.Telegram.Enabled {
		return errors.New("enable at least one alert delivery channel")
	}
	return errors.Join(failures...)
}

func (m *Monitor) TestChannel(ctx context.Context, settings model.OperatorAlertSettings, channel string) error {
	const subject = "WPX test alert"
	switch channel {
	case "smtp":
		if err := model.ValidateSMTPConfig(settings.SMTP); err != nil {
			return err
		}
		if err := m.sender.Send(ctx, settings.SMTP, subject, "WPX email alerts are configured correctly."); err != nil {
			return errors.New("SMTP test delivery failed")
		}
		return nil
	case "slack":
		if err := model.ValidateSlackAlertConfig(settings.Slack); err != nil {
			return err
		}
		if err := m.channels.SendSlack(ctx, settings.Slack, subject, "WPX Slack alerts are configured correctly."); err != nil {
			return errors.New("Slack test delivery failed")
		}
		return nil
	case "telegram":
		if err := model.ValidateTelegramAlertConfig(settings.Telegram); err != nil {
			return err
		}
		if err := m.channels.SendTelegram(ctx, settings.Telegram, subject, "WPX Telegram alerts are configured correctly."); err != nil {
			return errors.New("Telegram test delivery failed")
		}
		return nil
	default:
		return errors.New("unknown alert delivery channel")
	}
}

func resourceDetail(snapshot resources.Snapshot) string {
	lines := []string{fmt.Sprintf("CPU %.1f%%; memory %.1f%%; load %.2f %.2f %.2f; swap %d/%d bytes", snapshot.Current.CPUPercent, snapshot.Current.MemoryPercent, snapshot.Current.Load1, snapshot.Current.Load5, snapshot.Current.Load15, snapshot.Current.SwapUsed, snapshot.Current.SwapTotal)}
	for _, disk := range snapshot.Current.Disks {
		if disk.Error == "" {
			lines = append(lines, fmt.Sprintf("disk %s %d/%d bytes", disk.Path, disk.Used, disk.Total))
		}
	}
	sort.Strings(lines[1:])
	return strings.Join(lines, "\n")
}

func bounded(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum] + "\n[truncated]"
}
