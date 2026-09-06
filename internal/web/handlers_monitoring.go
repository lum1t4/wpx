package web

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/monitor"
	"github.com/lum1t4/wpx/internal/store"
)

type monitoringDisk struct {
	Path, Total, Used, Available, Error string
	Percent                             float64
}

type monitoringView struct {
	AutoRefresh, Available, CPUReady, NetworkReady bool
	Error, UpdatedAt, UpdatedISO, AttemptedAt      string
	CPU, Memory, MemoryTotal, Swap, SwapTotal      string
	Receive, Send, NetworkPeak, Load, Uptime       string
	CPUCount                                       int
	CPUPath, MemoryPath, ReceivePath, SendPath     string
	RangeStart, RangeEnd                           string
	Disks                                          []monitoringDisk
}

// Monitoring never makes a broker call or samples on demand. A busy dashboard
// cannot increase collection frequency, and customers cannot infer usage from
// their neighbours: the route requires the server-management capability.
func (s *Server) monitoringPage(w http.ResponseWriter, r *http.Request, user store.User) {
	snapshot := s.resources.Snapshot()
	view := newMonitoringView(snapshot)
	view.AutoRefresh = r.URL.Query().Get("refresh") != "off"
	status := http.StatusOK
	if !view.Available {
		status = http.StatusServiceUnavailable
		if view.Error == "" {
			view.Error = "The first resource sample is not available yet. The panel samples every 10 seconds."
		}
	}
	s.renderStatus(w, "monitoring.html", status, pageData{Title: "Monitoring", User: &user, CSRF: s.ensureCSRF(w, r), Monitoring: &view})
}

func newMonitoringView(snapshot monitor.Snapshot) monitoringView {
	current := snapshot.Current
	view := monitoringView{
		Available: !current.At.IsZero(), CPUReady: current.CPUReady, NetworkReady: current.NetworkReady,
		Error: snapshot.Error, CPUCount: current.CPUCount,
		CPU:    fmt.Sprintf("%.1f%%", current.CPUPercent),
		Memory: humanBytes(int64(current.MemoryUsed)), MemoryTotal: humanBytes(int64(current.MemoryTotal)),
		Swap: humanBytes(int64(current.SwapUsed)), SwapTotal: humanBytes(int64(current.SwapTotal)),
		Receive: humanBytes(int64(current.ReceiveRate)) + "/s", Send: humanBytes(int64(current.SendRate)) + "/s",
		Load:   fmt.Sprintf("%.2f / %.2f / %.2f", current.Load1, current.Load5, current.Load15),
		Uptime: formatUptime(current.Uptime),
	}
	if view.Available {
		view.UpdatedAt = current.At.UTC().Format("02 Jan 2006, 15:04:05 UTC")
		view.UpdatedISO = current.At.UTC().Format(time.RFC3339)
	}
	if !snapshot.AttemptedAt.IsZero() {
		view.AttemptedAt = snapshot.AttemptedAt.UTC().Format("15:04:05 UTC")
	}
	for _, disk := range current.Disks {
		item := monitoringDisk{Path: disk.Path, Total: humanBytes(int64(disk.Total)), Used: humanBytes(int64(disk.Used)), Available: humanBytes(int64(disk.Available)), Error: disk.Error}
		if disk.Total > 0 {
			item.Percent = 100 * float64(disk.Used) / float64(disk.Total)
		}
		view.Disks = append(view.Disks, item)
	}
	if len(snapshot.History) == 0 {
		return view
	}
	view.RangeStart = snapshot.History[0].At.UTC().Format("15:04:05")
	view.RangeEnd = snapshot.History[len(snapshot.History)-1].At.UTC().Format("15:04:05 UTC")
	view.CPUPath = metricPath(snapshot.History, 100, func(p monitor.Point) (float64, bool) { return p.CPUPercent, p.CPUReady })
	view.MemoryPath = metricPath(snapshot.History, 100, func(p monitor.Point) (float64, bool) { return p.MemoryPercent, p.MemoryReady })
	peak := 1.0
	for _, point := range snapshot.History {
		if point.NetworkReady {
			peak = max(peak, point.ReceiveRate, point.SendRate)
		}
	}
	view.NetworkPeak = humanBytes(int64(peak)) + "/s"
	view.ReceivePath = metricPath(snapshot.History, peak, func(p monitor.Point) (float64, bool) { return p.ReceiveRate, p.NetworkReady })
	view.SendPath = metricPath(snapshot.History, peak, func(p monitor.Point) (float64, bool) { return p.SendRate, p.NetworkReady })
	return view
}

// SVG paths contain only numbers computed here; no template.HTML or trusted
// user input is involved. Missing readings start a new segment, so a restart,
// read failure, or network counter reset is visible as a gap rather than zero.
func metricPath(history []monitor.Point, ceiling float64, value func(monitor.Point) (float64, bool)) string {
	if len(history) < 2 || ceiling <= 0 {
		return ""
	}
	start, end := history[0].At, history[len(history)-1].At
	span := end.Sub(start).Seconds()
	if span <= 0 {
		return ""
	}
	var path strings.Builder
	connected := false
	var previous time.Time
	for _, point := range history {
		number, ready := value(point)
		if !ready || math.IsNaN(number) || math.IsInf(number, 0) {
			connected = false
			continue
		}
		if !previous.IsZero() && (point.At.Sub(previous) > 3*monitor.SampleInterval || !point.At.After(previous)) {
			connected = false
		}
		command := "M"
		if connected {
			command = "L"
		}
		x := 8 + 584*point.At.Sub(start).Seconds()/span
		y := 132 - 124*max(0, min(number/ceiling, 1))
		fmt.Fprintf(&path, "%s%.1f %.1f ", command, x, y)
		connected, previous = true, point.At
	}
	return strings.TrimSpace(path.String())
}

func formatUptime(duration time.Duration) string {
	minutes := int64(duration / time.Minute)
	if minutes < 1 {
		return "Less than a minute"
	}
	days, hours := minutes/(24*60), (minutes/60)%24
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes%60)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes%60)
	}
	return fmt.Sprintf("%dm", minutes)
}
