package hostmetrics

import "testing"

func TestProcSamples(t *testing.T) {
	cpu := parseCPU("cpu 10 20 30 40 50 60 70 80 90 100\ncpu0 1 2 3 4")
	if cpu == nil || cpu.total != 360 || cpu.idle != 90 {
		t.Fatalf("guest time double-counted: %+v", cpu)
	}
	if parseCPU("cpu 1 ? 2 3") != nil || parseCPU("cpu0 1 2 3 4") != nil {
		t.Fatal("invalid counters accepted")
	}
	memory := parseMemory("MemTotal: 100 kB\nMemAvailable: 40 kB\nCached: 20 kB\n")
	if memory == nil || memory.Used != 60*1024 || memory.Total != 100*1024 {
		t.Fatalf("memory: %+v", memory)
	}
	for _, raw := range []string{"MemTotal: 100 kB", "MemTotal: 100 kB\nMemAvailable: 101 kB", "MemTotal: 100 kB\nMemAvailable: bad kB"} {
		if parseMemory(raw) != nil {
			t.Fatal("missing or invalid memory available must not imply fully used")
		}
	}
}
