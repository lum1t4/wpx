package alerts

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	resources "github.com/lum1t4/wpx/internal/monitor"
	"github.com/lum1t4/wpx/internal/store"
)

type fakeResources struct{ snapshot resources.Snapshot }

func (f *fakeResources) Snapshot() resources.Snapshot { return f.snapshot }

type sentMail struct{ subject, body string }
type fakeSender struct {
	mail []sentMail
	err  error
}

func (f *fakeSender) Send(_ context.Context, _ model.SMTPConfig, subject, body string) error {
	if f.err != nil {
		return f.err
	}
	f.mail = append(f.mail, sentMail{subject, body})
	return nil
}

type fakeBroker struct {
	diagnostics broker.OperatorDiagnosticsResult
	calls       []broker.Operation
}

func (f *fakeBroker) Call(_ context.Context, op broker.Operation, _ string, _ any, out any) error {
	f.calls = append(f.calls, op)
	if op == broker.OpOperatorDiagnostics {
		*out.(*broker.OperatorDiagnosticsResult) = f.diagnostics
	} else {
		*out.(*broker.OperatorEnsureSwapResult) = broker.OperatorEnsureSwapResult{}
	}
	return nil
}

func alertTestMonitor(t *testing.T) (*Monitor, *fakeResources, *fakeSender, *fakeBroker) {
	t.Helper()
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	if err := state.ConfigureSecretKey([]byte(strings.Repeat("a", 32))); err != nil {
		t.Fatal(err)
	}
	owner, err := state.CreateOwner(context.Background(), "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultOperatorAlertSettings()
	settings.Enabled = true
	settings.Memory = false
	settings.Disk = false
	settings.Services = false
	settings.SSLExpiry = false
	settings.Updates = false
	settings.OOM = false
	settings.SMTP = model.SMTPConfig{Host: "smtp.example.com", Port: 587, Transport: model.SMTPSTARTTLS, From: "alerts@example.com", To: "ops@example.com"}
	if err := state.SaveOperatorAlertSettings(context.Background(), owner, settings); err != nil {
		t.Fatal(err)
	}
	resourcesFake := &fakeResources{snapshot: resources.Snapshot{Current: resources.Sample{Point: resources.Point{CPUReady: true, CPUPercent: 95, MemoryReady: true}, MemoryTotal: 100}}}
	sender := &fakeSender{}
	privileged := &fakeBroker{}
	monitor := New(state, resourcesFake, privileged, slog.New(slog.NewTextHandler(io.Discard, nil)))
	monitor.sender = sender
	fixed := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	monitor.now = func() time.Time { return fixed }
	return monitor, resourcesFake, sender, privileged
}

func TestResourceAlertUsesHysteresisAndPersistsRecovery(t *testing.T) {
	monitor, resource, sender, _ := alertTestMonitor(t)
	ctx := context.Background()
	monitor.Evaluate(ctx)
	monitor.Evaluate(ctx)
	if len(sender.mail) != 0 {
		t.Fatal("alert sent before third bad sample")
	}
	monitor.Evaluate(ctx)
	if len(sender.mail) != 1 {
		t.Fatalf("alerts=%d", len(sender.mail))
	}
	monitor.Evaluate(ctx)
	if len(sender.mail) != 1 {
		t.Fatal("active condition repeated")
	}
	resource.snapshot.Current.CPUPercent = 20
	monitor.Evaluate(ctx)
	if len(sender.mail) != 1 {
		t.Fatal("resolved before second good sample")
	}
	monitor.Evaluate(ctx)
	if len(sender.mail) != 2 || !strings.Contains(sender.mail[1].subject, "resolved") {
		t.Fatalf("mail=%#v", sender.mail)
	}
}

func TestOOMEventsAreFingerprintDeduplicatedAndCooledDown(t *testing.T) {
	monitor, _, sender, _ := alertTestMonitor(t)
	settings, _ := monitor.store.OperatorAlertSettings(context.Background())
	settings.OOM = true
	settings.CooldownMins = 60
	monitor.event(context.Background(), settings, "oom", "WPX alert: OOM", "Killed process 123", "snapshot")
	monitor.event(context.Background(), settings, "oom", "WPX alert: OOM", "Killed process 123", "snapshot")
	monitor.event(context.Background(), settings, "oom", "WPX alert: OOM", "Killed process 456", "snapshot")
	if len(sender.mail) != 1 {
		t.Fatalf("cooldown allowed %d messages", len(sender.mail))
	}
	monitor.now = func() time.Time { return time.Date(2026, 9, 8, 13, 1, 0, 0, time.UTC) }
	monitor.event(context.Background(), settings, "oom", "WPX alert: OOM", "Killed process 456", "snapshot")
	if len(sender.mail) != 2 {
		t.Fatal("new event remained suppressed after cooldown")
	}
}
