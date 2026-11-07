package hostmetrics

import (
	"runtime"
	"testing"
	"time"
)

func TestCPURequiresTwoValidSamples(t *testing.T) {
	r := &Reader{}
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	sample := func(times *cpuTimes) *float64 {
		now = now.Add(5 * time.Second)
		return r.cpuUsage(times, now)
	}
	if got := sample(&cpuTimes{total: 100, idle: 80}); got != nil {
		t.Fatal("first CPU sample must be unknown")
	}
	if got := sample(&cpuTimes{total: 200, idle: 100}); got == nil || *got != 80 {
		t.Fatalf("delta CPU = %v, want 80", got)
	}
	if got := sample(&cpuTimes{total: 300, idle: 200}); got == nil || *got != 0 {
		t.Fatal("idle machine must report known zero")
	}
	if sample(&cpuTimes{total: 1, idle: 0}) != nil {
		t.Fatal("reset counters must be unknown")
	}
	if sample(nil) != nil {
		t.Fatal("read failure must be unknown")
	}
	if sample(&cpuTimes{total: 500, idle: 250}) != nil {
		t.Fatal("failure must clear CPU baseline")
	}
	if sample(&cpuTimes{total: 510, idle: 500}) != nil {
		t.Fatal("invalid idle delta must be unknown")
	}
}

func TestCPUReestablishesBaselineAfterSamplingGap(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	for _, gap := range []time.Duration{time.Minute, time.Hour, -time.Second, 0} {
		t.Run(gap.String(), func(t *testing.T) {
			r := &Reader{}
			r.cpuUsage(&cpuTimes{total: 100, idle: 80}, now)
			resumedAt := now.Add(gap)
			if got := r.cpuUsage(&cpuTimes{total: 10000, idle: 9000}, resumedAt); got != nil {
				t.Fatalf("CPU across interrupted sampling must be unknown, got %v", *got)
			}
			if got := r.cpuUsage(&cpuTimes{total: 10100, idle: 9020}, resumedAt.Add(5*time.Second)); got == nil || *got != 80 {
				t.Fatalf("CPU must use only fresh interval after resume, got %v", got)
			}
		})
	}
}

func TestLocalResourcesAndMissingDataPartition(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("unsupported platform")
	}
	r := &Reader{}
	dir := t.TempDir()
	s := r.Read(dir)
	if s.SampledAt.IsZero() || s.CPUPercent != nil {
		t.Fatal("first sample timestamp/CPU invalid")
	}
	if s.Memory == nil || s.Memory.Total == 0 || s.Memory.Used > s.Memory.Total {
		t.Fatalf("memory unavailable: %+v", s.Memory)
	}
	if s.Disk == nil || s.Disk.Total == 0 || s.Disk.Used > s.Disk.Total {
		t.Fatalf("disk unavailable: %+v", s.Disk)
	}
	if s = r.Read(dir + "/does-not-exist/partition"); s.Disk != nil {
		t.Fatal("unknown partition must not be reported as zero or root disk")
	}
}
