package client

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestCorePauseAcknowledgesModeAndClosesOnlyForwardedBusiness(t *testing.T) {
	mode := "Rule"
	var calls []string
	core := metricCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("unauthenticated control")
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "PATCH":
			var body struct {
				Mode string `json:"mode"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			mode = body.Mode
			w.WriteHeader(204)
		case r.URL.Path == "/configs":
			json.NewEncoder(w).Encode(map[string]string{"mode": mode})
		case r.URL.Path == "/connections":
			w.Write([]byte(`{"connections":[{"id":"business","chains":["node-A","proxy"],"metadata":{"type":"mixed/mixed-in"}},{"id":"probe","chains":["proxy"],"metadata":{"type":"mixed/probe-in"}},{"id":"management","chains":["direct"],"metadata":{"type":"tun/tun-in","host":"api-hqip.sharevps.online"}},{"id":"direct-business","chains":["direct"],"metadata":{"type":"mixed/mixed-in","host":"ordinary.example"}}]}`))
		case r.Method == "DELETE":
			if r.URL.Path != "/connections/business" && r.URL.Path != "/connections/direct-business" {
				t.Error("closed management or probe")
			}
			w.WriteHeader(204)
		default:
			t.Error("unexpected request")
		}
	})
	core.controlURL = DefaultServerURL
	if err := core.SetSuspended(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if mode != suspendedClashMode || len(calls) != 5 {
		t.Fatalf("missing pause acknowledgement: %v", calls)
	}
	if err := core.SetSuspended(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if mode != "Rule" {
		t.Fatal("did not resume rule routing")
	}
}

func TestCorePauseRefusesUnacknowledgedMode(t *testing.T) {
	core := metricCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			w.WriteHeader(204)
		} else {
			w.Write([]byte(`{"mode":"Rule"}`))
		}
	})
	if core.SetSuspended(context.Background(), true) == nil {
		t.Fatal("ignored mode was accepted")
	}
}

func TestSlowPauseDoesNotHoldCoreLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	core := metricCore(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.WriteHeader(204)
	})
	done := make(chan error, 1)
	go func() { done <- core.SetSuspended(context.Background(), true) }()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- core.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("pause held core lock")
	}
	close(release)
	if <-done == nil {
		t.Fatal("stale pause accepted")
	}
}
