package monitor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func sampleAt(at time.Time, counter uint64) Raw {
	return Raw{
		At: at, CPU: CPUCounters{Total: counter * 10, Busy: counter * 4}, CPUCount: 2,
		MemoryTotal: 1000, MemoryAvailable: 400, SwapTotal: 200, SwapFree: 150,
		Network: map[string]NetworkCounters{"eth0": {Received: counter * 100, Sent: counter * 50}},
		Disks:   []Disk{{Path: "/", Total: 2000, Used: 1200, Available: 600}},
	}
}

func TestRatesNeedTwoGoodConsecutiveSamples(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	raw := sampleAt(at, 100)
	var failure error
	m := New(func() (Raw, error) { return raw, failure })
	m.now = func() time.Time { return raw.At }
	m.Collect()
	first := m.Snapshot().Current
	if first.CPUReady || first.NetworkReady || first.MemoryUsed != 600 || first.SwapUsed != 50 || first.MemoryPercent != 60 {
		t.Fatalf("first sample incorrectly presents rates or memory: %+v", first)
	}
	raw = sampleAt(at.Add(SampleInterval), 200)
	m.Collect()
	second := m.Snapshot().Current
	if !second.CPUReady || second.CPUPercent != 40 || !second.NetworkReady || second.ReceiveRate != 1000 || second.SendRate != 500 {
		t.Fatalf("incorrect rates: %+v", second)
	}
	raw.At = raw.At.Add(SampleInterval)
	failure = errors.New("proc read denied")
	m.Collect()
	failed := m.Snapshot()
	if failed.Error != failure.Error() || !failed.Current.At.Equal(second.At) || failed.History[len(failed.History)-1].MemoryReady {
		t.Fatalf("failure did not preserve stale reading and add a gap: %+v", failed)
	}
	failure, raw = nil, sampleAt(raw.At.Add(SampleInterval), 400)
	m.Collect()
	recovered := m.Snapshot()
	if recovered.Error != "" || recovered.Current.CPUReady || recovered.Current.NetworkReady || !recovered.Current.MemoryReady {
		t.Fatalf("recovery must re-establish rate baseline: %+v", recovered)
	}
	raw = sampleAt(raw.At.Add(SampleInterval), 500)
	m.Collect()
	if !m.Snapshot().Current.NetworkReady {
		t.Fatal("second sample after recovery did not restore rate")
	}
}

func TestCPUAndNetworkDiscontinuitiesDoNotCreateSpikes(t *testing.T) {
	for _, test := range []struct {
		name         string
		edit         func(*Raw)
		cpu, network bool
	}{
		{"cpu reset", func(r *Raw) { r.CPU.Total, r.CPU.Busy = 1, 1 }, false, true},
		{"cpu idle", func(r *Raw) { r.CPU.Busy = 400 }, true, true},
		{"cpu busy impossible", func(r *Raw) { r.CPU.Busy = 3000 }, false, true},
		{"cpu stalled", func(r *Raw) { r.CPU.Total = 1000 }, false, true},
		{"network reset", func(r *Raw) { r.Network["eth0"] = NetworkCounters{Received: 1, Sent: 1} }, true, false},
		{"new interface", func(r *Raw) { r.Network["eth1"] = NetworkCounters{Received: 9000} }, true, false},
		{"removed interface", func(r *Raw) { delete(r.Network, "eth0") }, true, false},
		{"renamed interface", func(r *Raw) { r.Network["eth1"] = r.Network["eth0"]; delete(r.Network, "eth0") }, true, false},
		{"long suspension", func(r *Raw) { r.At = r.At.Add(time.Minute) }, false, false},
		{"clock backwards", func(r *Raw) { r.At = r.At.Add(-time.Minute) }, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
			raw := sampleAt(at, 100)
			m := New(func() (Raw, error) { return raw, nil })
			m.Collect()
			raw = sampleAt(at.Add(SampleInterval), 200)
			test.edit(&raw)
			m.Collect()
			current := m.Snapshot().Current
			if current.CPUReady != test.cpu || current.NetworkReady != test.network {
				t.Fatalf("CPUReady=%t NetworkReady=%t, want %t %t", current.CPUReady, current.NetworkReady, test.cpu, test.network)
			}
		})
	}
}

func TestHistoryIsBoundedAndCopiesDoNotMutateCollector(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	raw := sampleAt(at, 100)
	m := New(func() (Raw, error) { return raw, nil })
	for i := range HistoryLimit * 3 {
		raw = sampleAt(at.Add(time.Duration(i)*SampleInterval), uint64(i))
		m.Collect()
	}
	snapshot := m.Snapshot()
	if len(snapshot.History) != HistoryLimit || snapshot.History[len(snapshot.History)-1].At.Sub(snapshot.History[0].At) != HistoryWindow {
		t.Fatalf("history is not a fixed hour: %d samples", len(snapshot.History))
	}
	snapshot.History[0].MemoryPercent = 900
	snapshot.Current.Disks[0].Path = "changed"
	raw.Network["eth0"] = NetworkCounters{}
	if m.Snapshot().History[0].MemoryPercent == 900 || m.Snapshot().Current.Disks[0].Path == "changed" || m.previous.Network["eth0"].Received == 0 {
		t.Fatal("snapshot or raw reader data aliases the collector")
	}
	raw = sampleAt(raw.At.Add(2*HistoryWindow), 9000)
	m.Collect()
	if len(m.Snapshot().History) != 1 {
		t.Fatal("samples older than one hour were retained")
	}
	raw.At = raw.At.Add(-time.Hour)
	m.Collect()
	if len(m.Snapshot().History) != 1 {
		t.Fatal("clock reset retained backwards history")
	}
}

func TestHistoryHardLimitAlsoBoundsRapidManualCollection(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	index := 0
	m := New(func() (Raw, error) {
		index++
		return sampleAt(at.Add(time.Duration(index)*time.Millisecond), uint64(index)), nil
	})
	for range HistoryLimit * 2 {
		m.Collect()
	}
	if len(m.Snapshot().History) != HistoryLimit {
		t.Fatal("manual collection bypassed history capacity")
	}
}

func TestRunStopsWithContextAndConcurrentSnapshotsAreSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sampled := make(chan struct{}, 1)
	m := New(func() (Raw, error) {
		select {
		case sampled <- struct{}{}:
		default:
		}
		return sampleAt(time.Now(), 100), nil
	})
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	select {
	case <-sampled:
	case <-time.After(time.Second):
		t.Fatal("sampler did not take initial reading")
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 100 {
				_ = m.Snapshot()
			}
		})
	}
	for range 100 {
		m.Collect()
	}
	readers.Wait()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sampler ignored cancellation")
	}
}
