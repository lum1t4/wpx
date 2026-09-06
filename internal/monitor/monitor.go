package monitor

import (
	"context"
	"sync"
	"time"
)

// Monitor owns a fixed-size history and the previous counter sample. Collection
// is independent of page requests: opening more tabs does not read /proc more
// often, allocate more history, or change the meaning of a CPU interval.
type Monitor struct {
	mu       sync.RWMutex
	collect  sync.Mutex
	read     func() (Raw, error)
	now      func() time.Time
	previous *Raw
	state    Snapshot
	running  bool
}

func New(read func() (Raw, error)) *Monitor {
	return &Monitor{read: read, now: time.Now, state: Snapshot{History: make([]Point, 0, HistoryLimit)}}
}

// Run must share the panel's lifetime. No goroutine is started by construction,
// so tests and short-lived commands cannot accidentally leak a sampler.
func (m *Monitor) Run(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()
	if ctx.Err() != nil {
		return
	}
	m.Collect()
	ticker := time.NewTicker(SampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Collect()
		}
	}
}

// Collect is exported for deterministic checks and one-shot diagnostics. Normal
// HTTP handlers read Snapshot only. A failed read breaks the counter baseline;
// the next successful sample must not draw a rate across the missing interval.
func (m *Monitor) Collect() {
	m.collect.Lock()
	defer m.collect.Unlock()
	raw, err := m.read()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.state.AttemptedAt = m.now()
		m.state.Error = err.Error()
		m.previous = nil
		m.append(Point{At: m.state.AttemptedAt})
		return
	}
	point := Point{At: raw.At, MemoryReady: raw.MemoryTotal > 0}
	memoryUsed := raw.MemoryTotal - min(raw.MemoryAvailable, raw.MemoryTotal)
	if point.MemoryReady {
		point.MemoryPercent = 100 * float64(memoryUsed) / float64(raw.MemoryTotal)
	}
	if previous := m.previous; previous != nil {
		elapsed := raw.At.Sub(previous.At)
		// A delayed tick or clock discontinuity is a gap, not a normal ten-second
		// sample. This also avoids claiming a current rate after process suspension.
		if elapsed > 0 && elapsed <= 3*SampleInterval {
			point.CPUPercent, point.CPUReady = cpuRate(previous.CPU, raw.CPU)
			point.ReceiveRate, point.SendRate, point.NetworkReady = networkRates(previous.Network, raw.Network, elapsed.Seconds())
		}
	}
	m.state.Current = Sample{
		Point: point, CPUCount: raw.CPUCount,
		MemoryTotal: raw.MemoryTotal, MemoryUsed: memoryUsed,
		SwapTotal: raw.SwapTotal, SwapUsed: raw.SwapTotal - min(raw.SwapFree, raw.SwapTotal),
		Load1: raw.Load1, Load5: raw.Load5, Load15: raw.Load15, Uptime: raw.Uptime,
		Disks: append([]Disk(nil), raw.Disks...),
	}
	m.state.AttemptedAt, m.state.Error = raw.At, ""
	m.append(point)
	// Only counters are needed for the next interval; do not retain raw disk
	// errors and filesystem records a second time.
	previous := Raw{At: raw.At, CPU: raw.CPU, Network: make(map[string]NetworkCounters, len(raw.Network))}
	for name, counters := range raw.Network {
		previous.Network[name] = counters
	}
	m.previous = &previous
}

func (m *Monitor) append(point Point) {
	cutoff := point.At.Add(-HistoryWindow)
	history := m.state.History
	if len(history) > 0 && !point.At.After(history[len(history)-1].At) {
		// A clock discontinuity cannot leave history ordered newest-to-oldest.
		// Starting again is clearer than drawing a chart backwards in time.
		history = history[:0]
	}
	start := 0
	for start < len(history) && history[start].At.Before(cutoff) {
		start++
	}
	if start > 0 {
		copy(history, history[start:])
		history = history[:len(history)-start]
	}
	if len(history) == HistoryLimit {
		copy(history, history[1:])
		history = history[:HistoryLimit-1]
	}
	m.state.History = append(history, point)
}

// Snapshot copies the small presentation state. Callers cannot mutate the
// sampler's history while it is collecting in another goroutine.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := m.state
	result.History = append([]Point(nil), result.History...)
	result.Current.Disks = append([]Disk(nil), result.Current.Disks...)
	return result
}

func cpuRate(before, after CPUCounters) (float64, bool) {
	if after.Total <= before.Total || after.Busy < before.Busy {
		return 0, false
	}
	total, busy := after.Total-before.Total, after.Busy-before.Busy
	if busy > total {
		return 0, false
	}
	return 100 * float64(busy) / float64(total), true
}

func networkRates(before, after map[string]NetworkCounters, seconds float64) (float64, float64, bool) {
	if len(before) == 0 || len(before) != len(after) || seconds <= 0 {
		return 0, 0, false
	}
	var received, sent float64
	for name, current := range after {
		previous, exists := before[name]
		if !exists || current.Received < previous.Received || current.Sent < previous.Sent {
			return 0, 0, false
		}
		received += float64(current.Received - previous.Received)
		sent += float64(current.Sent - previous.Sent)
	}
	return received / seconds, sent / seconds, true
}
