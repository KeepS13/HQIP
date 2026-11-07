package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/hostmetrics"
)

func TestHostResourceCacheDoesNotBlockOrDuplicateSamples(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{}, 2)
	cache := &hostResourceCache{read: func() hostmetrics.Sample {
		calls.Add(1)
		<-gate
		return hostmetrics.Sample{SampledAt: time.Now(), Memory: &hostmetrics.Usage{Used: 5, Total: 10}}
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
