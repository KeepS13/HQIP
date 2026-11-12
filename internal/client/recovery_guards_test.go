package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecoveryTerminalAuthorizationReasons(t *testing.T) {
	for _, kind := range []string{"exhausted", "expired", "removed", "disabled", "account", "node", "integrity"} {
		t.Run(kind, func(t *testing.T) {
			f := newRecoveryFixture(t)
			r := f.recovery()
			f.mu.Lock()
			sub := *f.profile.Subscription
			f.profile.Subscription = &sub
			switch kind {
			case "exhausted":
				quota := int64(100)
				sub.QuotaBytes = &quota
				sub.UsedBytes = 100
			case "expired":
				sub.ExpiresAt = f.now.Add(-time.Second)
			case "removed":
				f.profile.Subscription = nil
			case "disabled":
				sub.Status = "disabled"
			case "account":
				f.profile.Enabled = false
			case "node":
				f.profile.Nodes = []Node{{ID: "B", Ready: true}}
			case "integrity":
				f.profile.Rules.Hash = strings.Repeat("0", 64)
			}
			f.mu.Unlock()
			_, err := f.c.recoveryAttempt(r)
			if err != nil && !retryable(err) {
				f.c.stopRecovery(r, err.Error())
			}
			waitState(t, f.c, "failed")
			v := f.c.View()
			if v.ConnectionIntent || v.LastError == "" || v.RunState != "failed" || f.e.Alive() {
				t.Fatalf("did not stop %s: %+v", kind, v.Status)
			}
			if kind == "exhausted" && !strings.Contains(v.LastError, "额度已耗尽") {
				t.Fatal("inaccurate quota reason")
			}
		})
	}
}

func TestRecoveryFirstConnectionFailureNeverBecomesEligible(t *testing.T) {
	c, e, _ := setupClient(t)
	e.fail = true
	_ = c.Connect()
	waitState(t, c, "failed")
	c.mu.Lock()
	c.startRecoveryLocked("lease_missing")
	eligible := c.recoveryReady || c.recovery != nil
	c.mu.Unlock()
	if eligible || c.View().LastError == "" {
		t.Fatal("first failure became silent")
	}
}

func TestRecoveryStaleLeaseAndProfileCannotStopNewGeneration(t *testing.T) {
	for _, resource := range []string{"connections", "profile"} {
		t.Run(resource, func(t *testing.T) {
			f := newRecoveryFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v3/client/"+resource {
					close(entered)
					<-release
					w.WriteHeader(403)
					return
				}
				if r.URL.Path == "/api/v3/client/refresh" {
					w.WriteHeader(403)
					return
				}
				w.WriteHeader(204)
			}))
			defer srv.Close()
			api, _ := NewAPI(srv.URL, Tokens{AccessToken: "test", RefreshToken: "test", AccessExpiresAt: time.Now().Add(time.Hour)})
			f.c.mu.Lock()
			f.c.api = api
			f.c.mu.Unlock()
			done := make(chan struct{})
			go func() {
				if resource == "profile" {
					_ = f.c.Sync(context.Background())
				} else {
					f.c.checkAccountConnection(context.Background())
				}
				close(done)
			}()
			<-entered
			f.c.mu.Lock()
			f.c.generation++
			f.c.view.RunState = "connected"
			f.c.view.ConnectionIntent = true
			f.c.mu.Unlock()
			close(release)
			<-done
			if v := f.c.View(); !v.ConnectionIntent || v.RunState != "connected" || v.LastError != "" {
				t.Fatal("late authorization result stopped a new generation")
			}
		})
	}
}

func TestRecoveryMetricsKeepTotalsWithoutStaleSpeed(t *testing.T) {
	engine := &sampleEngine{sample: CoreSample{UploadRate: 50, DownloadRate: 90, UploadTotal: 1000, DownloadTotal: 2000}}
	c, err := New(t.TempDir(), fakePlatform{}, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.view.RunState = "recovering"
	c.beginMetricsLocked()
	before := c.view.Metrics.ConnectedAt
	c.sampleMetrics(context.Background())
	engine.err = errors.New("injected sample outage")
	c.sampleMetrics(context.Background())
	m := c.View().Metrics
	if m.UploadRate != 0 || m.DownloadRate != 0 || m.UploadBytes != 1000 || m.DownloadBytes != 2000 || !m.Stale || !m.ConnectedAt.Equal(*before) {
		t.Fatal("stale rates or lost totals")
	}
}

func TestRecoveryStatusReportsActualState(t *testing.T) {
	f := newRecoveryFixture(t)
	f.recovery()
	reported := make(chan Status, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var s Status
		json.NewDecoder(r.Body).Decode(&s)
		reported <- s
		w.WriteHeader(204)
	}))
	defer srv.Close()
	api, _ := NewAPI(srv.URL, Tokens{AccessToken: "test", RefreshToken: "test", AccessExpiresAt: time.Now().Add(time.Hour)})
	f.c.mu.Lock()
	f.c.api = api
	f.c.mu.Unlock()
	f.c.report()
	s := <-reported
	if s.RunState != "recovering" || !s.ConnectionIntent || s.LastError != "" || s.ActualNodeID != "" || s.DesiredNodeID != "A" {
		t.Fatalf("misleading operator state %+v", s)
	}
}
