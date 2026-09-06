//go:build linux

package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/monitor"
	"github.com/lum1t4/wpx/internal/rbac"
)

func monitoringFixture(server *Server) {
	index := 0
	server.resources = monitor.New(func() (monitor.Raw, error) {
		index++
		return monitor.Raw{
			At:  time.Date(2026, 9, 6, 12, 0, index*10, 0, time.UTC),
			CPU: monitor.CPUCounters{Total: uint64(index * 100), Busy: uint64(index * 25)}, CPUCount: 2,
			MemoryTotal: 1024 * 1024 * 1024, MemoryAvailable: 512 * 1024 * 1024,
			Network: map[string]monitor.NetworkCounters{"eth0": {Received: uint64(index * 10240), Sent: uint64(index * 20480)}},
			Disks:   []monitor.Disk{{Path: "/", Total: 4 * 1024 * 1024 * 1024, Used: 1024 * 1024 * 1024, Available: 2 * 1024 * 1024 * 1024}, {Path: "/unavailable", Error: "permission denied"}},
			Load1:   0.25, Load5: 0.20, Load15: 0.15, Uptime: 25*time.Hour + 2*time.Minute,
		}, nil
	})
	server.resources.Collect()
	server.resources.Collect()
}

func TestMonitoringAuthorizationAndNavigation(t *testing.T) {
	server, owner, privileged := navigationServer(t)
	monitoringFixture(server)
	for _, role := range []rbac.Role{rbac.Administrator, rbac.Collaborator, rbac.Customer} {
		user, err := server.store.CreateUser(context.Background(), owner, "monitor-"+string(role), "monitoring-test-password", role, nil)
		if err != nil {
			t.Fatal(err)
		}
		status := http.StatusForbidden
		if role == rbac.Administrator {
			status = http.StatusOK
		}
		response := navigationRequest(t, server, user, http.MethodGet, "/monitoring", nil)
		requireNavigationStatus(t, response, status)
		overview := navigationRequest(t, server, user, http.MethodGet, "/", nil)
		requireNavigationStatus(t, overview, http.StatusOK)
		if got, want := navigationLinks(overview.Body.String())["/monitoring"], role == rbac.Administrator; got != want {
			t.Errorf("role %s monitoring link=%t, want %t", role, got, want)
		}
	}
	response := navigationRequest(t, server, owner, http.MethodGet, "/monitoring", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	for _, want := range []string{"25.0%", "512.0 MiB", "1.0 KiB/s", "2.0 KiB/s", "1d 1h 2m", "permission denied", "kept in memory only", `aria-current="page"`, `http-equiv="refresh" content="10"`, `d="M`} {
		if !strings.Contains(body, want) {
			t.Errorf("monitoring page missing %q", want)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("monitoring must not be cached")
	}
	if len(privileged.calls) != 0 {
		t.Fatalf("monitoring invoked privileged operations: %v", privileged.calls)
	}
	anonymous := httptest.NewRecorder()
	server.Handler().ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/monitoring", nil))
	if anonymous.Code != http.StatusSeeOther || anonymous.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous monitoring access: %d", anonymous.Code)
	}
}

func TestMonitoringPauseDoesNotStopCollectionOrReadAgain(t *testing.T) {
	server, owner, _ := navigationServer(t)
	reads := 0
	server.resources = monitor.New(func() (monitor.Raw, error) {
		reads++
		return monitor.Raw{At: time.Now(), MemoryTotal: 1024, MemoryAvailable: 512}, nil
	})
	server.resources.Collect()
	for range 3 {
		response := navigationRequest(t, server, owner, http.MethodGet, "/monitoring?refresh=off", nil)
		requireNavigationStatus(t, response, http.StatusOK)
		if strings.Contains(response.Body.String(), `http-equiv="refresh"`) || !strings.Contains(response.Body.String(), "collection continues") {
			t.Fatal("pause must disable page refresh and explain collection continues")
		}
	}
	if reads != 1 {
		t.Fatalf("page requests triggered %d kernel reads", reads)
	}
}

func TestMonitoringFailureIsExplicitAndNeverInventsZeroReadings(t *testing.T) {
	server, owner, _ := navigationServer(t)
	var failure error = errors.New("read meminfo: permission denied")
	server.resources = monitor.New(func() (monitor.Raw, error) {
		return monitor.Raw{At: time.Now(), MemoryTotal: 1024, MemoryAvailable: 512}, failure
	})
	server.resources.Collect()
	response := navigationRequest(t, server, owner, http.MethodGet, "/monitoring", nil)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "permission denied") || strings.Contains(response.Body.String(), "Memory in use") {
		t.Fatalf("initial failure invented resource data: %d %s", response.Code, response.Body.String())
	}
	failure = nil
	server.resources.Collect()
	failure = errors.New("read stat: temporarily unavailable")
	server.resources.Collect()
	response = navigationRequest(t, server, owner, http.MethodGet, "/monitoring", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), "last successful reading, not live data") || !strings.Contains(response.Body.String(), "512 B") {
		t.Fatal("transient failure did not mark the last known reading stale")
	}
}

func TestMetricPathsShowGapsAndStayInsideChart(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	history := []monitor.Point{
		{At: at, CPUPercent: 0, CPUReady: true},
		{At: at.Add(10 * time.Second), CPUPercent: 50, CPUReady: true},
		{At: at.Add(20 * time.Second)},
		{At: at.Add(30 * time.Second), CPUPercent: 100, CPUReady: true},
		{At: at.Add(40 * time.Second), CPUPercent: 200, CPUReady: true},
	}
	path := metricPath(history, 100, func(p monitor.Point) (float64, bool) { return p.CPUPercent, p.CPUReady })
	if path != "M8.0 132.0 L154.0 70.0 M446.0 8.0 L592.0 8.0" {
		t.Fatalf("path did not clamp values or split missing interval: %s", path)
	}
	if metricPath(history[:1], 100, func(p monitor.Point) (float64, bool) { return p.CPUPercent, p.CPUReady }) != "" {
		t.Fatal("one point must not pretend to be a history line")
	}
}
