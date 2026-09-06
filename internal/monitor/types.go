// Package monitor samples Linux host resources without privileged operations,
// an external service, or persistent metrics storage. Counters are converted to
// rates only when two consecutive, trustworthy samples exist.
package monitor

import "time"

const (
	SampleInterval = 10 * time.Second
	HistoryWindow  = time.Hour
	HistoryLimit   = int(HistoryWindow/SampleInterval) + 1
)

type CPUCounters struct{ Busy, Total uint64 }
type NetworkCounters struct{ Received, Sent uint64 }

// Used excludes free blocks; Available also excludes blocks reserved by the
// filesystem. They therefore need not add up to Total.
type Disk struct {
	Path                   string
	Total, Used, Available uint64
	Error                  string
}

// Raw retains per-interface counters so adding, removing, or resetting an
// interface cannot turn a discontinuity into a traffic spike.
type Raw struct {
	At                                                time.Time
	CPU                                               CPUCounters
	CPUCount                                          int
	MemoryTotal, MemoryAvailable, SwapTotal, SwapFree uint64
	Network                                           map[string]NetworkCounters
	Load1, Load5, Load15                              float64
	Uptime                                            time.Duration
	Disks                                             []Disk
}

// Point is deliberately small: history never retains filesystem records,
// interface maps, or an unbounded set of labels for every sample.
type Point struct {
	At                                  time.Time
	CPUPercent, MemoryPercent           float64
	ReceiveRate, SendRate               float64
	CPUReady, MemoryReady, NetworkReady bool
}

type Sample struct {
	Point
	CPUCount                                     int
	MemoryTotal, MemoryUsed, SwapTotal, SwapUsed uint64
	Load1, Load5, Load15                         float64
	Uptime                                       time.Duration
	Disks                                        []Disk
}

type Snapshot struct {
	Current     Sample
	History     []Point
	AttemptedAt time.Time
	Error       string
}
