package control

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/hostmetrics"
)

func TestHostResourcesRealCPUBecomesReadyWithinFirstRound(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("supported CPU source required")
	}
	reader := &hostmetrics.Reader{}
	dir := t.TempDir()
	cache := &hostResourceCache{read: func() hostmetrics.Sample { return reader.Read(dir) }}
	start := time.Now()
	for {
		view := cache.snapshot(start) // Never advance to the next five-second round.
		if !view.Refreshing && view.Sample != nil && view.Sample.CPUPercent != nil {
			t.Logf("real CPU available in %s", time.Since(start).Round(time.Millisecond))
			return
		}
		if time.Since(start) > 4*time.Second {
			t.Fatal("first CPU value waited for the next polling round")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestHostResourceCacheDoesNotBlockOrDuplicateSamples(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{}, 2)
	cache := &hostResourceCache{read: func() hostmetrics.Sample {
		calls.Add(1)
		<-gate
		cpu := 0.0 // A valid idle sample needs no baseline warmup.
		return hostmetrics.Sample{SampledAt: time.Now(), CPUPercent: &cpu, Memory: &hostmetrics.Usage{Used: 5, Total: 10}}
	}}
	now := time.Now()
	for range 20 {
		v := cache.snapshot(now)
		if !v.Refreshing || v.Sample != nil {
			t.Fatalf("cold response = %+v", v)
		}
	}
	gate <- struct{}{}
	deadline := time.Now().Add(time.Second)
	var ready hostResourceView
	for {
		ready = cache.snapshot(now)
		if !ready.Refreshing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sample did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 || ready.Sample == nil {
		t.Fatal("cache did not coalesce simultaneous reads")
	}
	stale := cache.snapshot(now.Add(time.Minute))
	if !stale.Refreshing || stale.Sample != ready.Sample {
		t.Fatal("refresh must return last sample without waiting")
	}
	gate <- struct{}{}
}

func TestHostResourceCPUWarmupDoesNotWaitForAnotherPoll(t *testing.T) {
	for _, cpuAvailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "cold-and-resumed", false: "unsupported-bounded"}[cpuAvailable], func(t *testing.T) {
			var calls atomic.Int32
			cache := &hostResourceCache{warmup: time.Millisecond, read: func() hostmetrics.Sample {
				call := calls.Add(1)
				sample := hostmetrics.Sample{SampledAt: time.Now(), Memory: &hostmetrics.Usage{Used: 1, Total: 2}}
				if cpuAvailable && call%2 == 0 {
					cpu := 42.0
					sample.CPUPercent = &cpu
				}
				return sample
			}}
			for i := range 2 {
				now := time.Now().Add(time.Duration(i) * time.Minute)
				cache.snapshot(now)
				deadline := time.Now().Add(time.Second)
				for {
					// Polling here only reads the same in-flight cache; it cannot
					// request the next 5-second sample or supply a CPU baseline.
					view := cache.snapshot(now)
					if !view.Refreshing {
						if view.Sample == nil || (view.Sample.CPUPercent != nil) != cpuAvailable || calls.Load() != int32((i+1)*2) {
							t.Fatalf("warmup result=%+v calls=%d", view, calls.Load())
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("CPU waited for next poll")
					}
					time.Sleep(time.Millisecond)
				}
			}
		})
	}
}

func TestHostResourcesRequireAdminAndStayOffPublicSurface(t *testing.T) {
	s, admin := newServer(t)
	path := "/api/v1/host-resources"
	if w := request(admin, "GET", path, nil, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status %d", w.Code)
	}
	cookie, csrf := loginAdmin(t, s, admin)
	if w := request(s.PublicHandler(), "GET", path, nil, cookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("public status %d", w.Code)
	}
	w := request(admin, "GET", path, nil, cookie, "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated read: %d %s", w.Code, w.Body.String())
	}
	var result hostResourceView
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal("invalid resource response")
	}
	if strings.Contains(w.Body.String(), s.Store.DataDir) {
		t.Fatal("response exposes data directory")
	}
	if w = request(admin, "POST", path, nil, cookie, csrf); w.Code == http.StatusOK {
		t.Fatal("resource endpoint must be read only")
	}
}
