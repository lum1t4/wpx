package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cpuFixture = "cpu  100 20 30 400 50 6 7 8 90 10\ncpu0 1 2 3 4\ncpu2 1 2 3 4\nintr 123\ncpuinvalid 99\n"
const memoryFixture = "MemTotal: 1000 kB\nMemAvailable: 600 kB\nSwapTotal: 200 kB\nSwapFree: 150 kB\nHugePages_Total: 0\n"
const networkFixture = "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n lo: 900 1 0 0 0 0 0 0 900 1 0 0 0 0 0 0\n eth0: 100 1 0 0 0 0 0 0 200 1 0 0 0 0 0 0\n ens3: 300 1 0 0 0 0 0 0 400 1 0 0 0 0 0 0\n"

func TestParseCPUExcludesGuestAndIOWait(t *testing.T) {
	cpu, count, err := parseCPU(cpuFixture)
	if err != nil {
		t.Fatal(err)
	}
	if cpu.Total != 621 || cpu.Busy != 171 || count != 2 {
		t.Fatalf("got CPU %+v and %d logical CPUs", cpu, count)
	}
}

func TestParseCPURejectsMissingMalformedAndOverflowingCounters(t *testing.T) {
	for _, data := range []string{
		"", "cpu 1 2 3 4\n", "cpu0 1 2 3 4\n", "cpu 1 2 3\ncpu0 1 2 3 4\n",
		"cpu 1 2 bad 4\ncpu0 1 2 3 4\n", "cpu 18446744073709551615 1 1 1\ncpu0 1 2 3 4\n",
		"cpu 1 2 3 4\ncpu 1 2 3 4\ncpu0 1 2 3 4\n",
	} {
		if _, _, err := parseCPU(data); err == nil {
			t.Errorf("accepted malformed CPU data %q", data)
		}
	}
}

func TestParseMemoryUsesAvailableAndAllowsNoSwap(t *testing.T) {
	total, available, swapTotal, swapFree, err := parseMemory(memoryFixture)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1000*1024 || available != 600*1024 || swapTotal != 200*1024 || swapFree != 150*1024 {
		t.Fatalf("wrong byte conversion: %d, %d, %d, %d", total, available, swapTotal, swapFree)
	}
	for _, suffix := range []string{"", "SwapTotal: 0 kB\nSwapFree: 0 kB\n"} {
		_, _, swapTotal, swapFree, err := parseMemory("MemTotal: 1000 kB\nMemAvailable: 100 kB\n" + suffix)
		if err != nil || swapTotal != 0 || swapFree != 0 {
			t.Fatalf("no swap should be zero: %d, %d, %v", swapTotal, swapFree, err)
		}
	}
}

func TestParseMemoryRejectsUnreliableTotals(t *testing.T) {
	for _, data := range []string{
		"", "MemTotal: 100 kB\n", "MemAvailable: 100 kB\n", "MemTotal: 0 kB\nMemAvailable: 0 kB\n",
		"MemTotal: 100 kB\nMemAvailable: 101 kB\n", "MemTotal: 100 MB\nMemAvailable: 10 kB\n",
		"MemTotal: 100 kB\nMemAvailable: -1 kB\n", "MemTotal: 18446744073709551615 kB\nMemAvailable: 0 kB\n",
		memoryFixture + "MemTotal: 1000 kB\n", "MemTotal: 1000 kB\nMemAvailable: 100 kB\nSwapTotal: 10 kB\n",
		"MemTotal: 1000 kB\nMemAvailable: 100 kB\nSwapTotal: 10 kB\nSwapFree: 11 kB\n",
	} {
		if _, _, _, _, err := parseMemory(data); err == nil {
			t.Errorf("accepted unreliable memory data %q", data)
		}
	}
}

func TestParseNetworkKeepsInterfaceCountersAndExcludesLoopback(t *testing.T) {
	network, err := parseNetwork(networkFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(network) != 2 || network["eth0"] != (NetworkCounters{Received: 100, Sent: 200}) || network["ens3"] != (NetworkCounters{Received: 300, Sent: 400}) {
		t.Fatalf("unexpected counters: %+v", network)
	}
	loopback, err := parseNetwork("lo: 900 1 0 0 0 0 0 0 900 1 0 0 0 0 0 0")
	if err != nil || len(loopback) != 0 {
		t.Fatalf("loopback-only host: %+v, %v", loopback, err)
	}
}

func TestParseNetworkRejectsMalformedCounters(t *testing.T) {
	for _, data := range []string{
		"", "Inter-| Receive | Transmit\n", "eth0: 1 2 3", "eth0: x 1 0 0 0 0 0 0 2 1 0 0 0 0 0 0",
		"eth0: -1 1 0 0 0 0 0 0 2 1 0 0 0 0 0 0", networkFixture + "eth0: 1 1 0 0 0 0 0 0 2 1 0 0 0 0 0 0",
	} {
		if _, err := parseNetwork(data); err == nil {
			t.Errorf("accepted malformed network data %q", data)
		}
	}
}

func TestParseLoadAndUptime(t *testing.T) {
	one, five, fifteen, err := parseLoad("0.12 1.34 2.56 1/500 1234\n")
	if err != nil || one != 0.12 || five != 1.34 || fifteen != 2.56 {
		t.Fatalf("unexpected load averages: %v, %v, %v, %v", one, five, fifteen, err)
	}
	uptime, err := parseUptime("1234.25 12000.50\n")
	if err != nil || uptime != 1234*time.Second+250*time.Millisecond {
		t.Fatalf("unexpected uptime: %v, %v", uptime, err)
	}
	for _, data := range []string{"", "1 2", "NaN 0 0", "0 +Inf 0", "0 0 -1", "x 0 0"} {
		if _, _, _, err := parseLoad(data); err == nil {
			t.Errorf("accepted invalid load %q", data)
		}
	}
	for _, data := range []string{"", "-1 0", "NaN 0", "+Inf 0", "100000000000 0", "x 0"} {
		if _, err := parseUptime(data); err == nil {
			t.Errorf("accepted invalid uptime %q", data)
		}
	}
}

func TestReadProcBoundsInputAndPreservesReadErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")
	if err := os.WriteFile(path, []byte(cpuFixture), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readProc(path); err != nil || got != cpuFixture {
		t.Fatalf("bounded read: %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxProcBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProc(path); err == nil || !strings.Contains(err.Error(), "read limit") {
		t.Fatalf("expected bounded read error, got %v", err)
	}
	if _, err := readProc(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatalf("expected missing file error, got %v", err)
	}
}
