package alerts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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
	logger    *slog.Logger
	now       func() time.Time
	interval  time.Duration
	mu        sync.Mutex
}

func New(state *store.Store, resourceMonitor resourceReader, privileged brokerCaller, logger *slog.Logger) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Monitor{store: state, resources: resourceMonitor, broker: privileged, sender: SMTPSender{}, logger: logger, now: time.Now, interval: CheckInterval}
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
		if state.BadSamples >= trigger && !state.Active && (state.LastSentAt.IsZero() || now.Sub(state.LastSentAt) >= cooldown) {
			if m.send(ctx, settings, "WPX alert: "+key, summary, detail) == nil {
				state.Active = true
				state.LastSentAt = now
			}
		}
	} else {
		state.BadSamples = 0
		if state.Active {
			state.GoodSamples++
			if state.GoodSamples >= 2 {
				if m.send(ctx, settings, "WPX resolved: "+key, summary+" The condition has recovered.", detail) == nil {
					state.Active = false
					state.GoodSamples = 0
					state.LastSentAt = now
				}
			}
		} else {
			state.GoodSamples = 0
		}
	}
	if err := m.store.SaveOperatorAlertState(ctx, state); err != nil {
		m.logger.Error("save alert state", "key", key, "error", err)
	}
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
		if m.send(ctx, settings, subject, event, detail) == nil {
			state.LastSentAt = now
			state.LastFingerprint = digest
		}
	}
	if err := m.store.SaveOperatorAlertState(ctx, state); err != nil {
		m.logger.Error("save alert event state", "key", key, "error", err)
	}
}

func (m *Monitor) send(ctx context.Context, settings model.OperatorAlertSettings, subject, summary, detail string) error {
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	if len(subject) > 160 {
		subject = subject[:160]
	}
	body := summary
	if strings.TrimSpace(detail) != "" {
		body += "\n\nBounded diagnostic snapshot:\n" + bounded(detail, 8192)
	}
	if err := m.sender.Send(ctx, settings.SMTP, subject, body); err != nil {
		m.logger.Error("send operator alert", "alert", subject, "error", err)
		return err
	}
	return nil
}

func (m *Monitor) Test(ctx context.Context, settings model.OperatorAlertSettings) error {
	if err := model.ValidateOperatorAlertSettings(settings); err != nil {
		return err
	}
	return m.sender.Send(ctx, settings.SMTP, "WPX test alert", "WPX SMTP alerts are configured correctly. No alert condition triggered this message.")
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
