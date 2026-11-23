package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The fixture emulates the authenticated Agent boundary, not the slot rule:
// each admission reserves one slot and only a received final releases it.
type activityAdmission struct {
	mu                sync.Mutex
	active            map[string]bool
	opened, finalized int
	deny              bool
}

func activityFixture(t *testing.T) (*Admission, *activityAdmission) {
	t.Helper()
	f := &activityAdmission{active: make(map[string]bool)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/admission/check" {
			_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true, "accountingId": "subscription-test"})
			return
		}
		var body struct {
			ConnectionID string `json:"connectionId"`
			Closed       bool   `json:"closed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path == "/admission" {
			if f.deny {
				http.Error(w, "full", 403)
				return
			}
			f.opened++
			id := fmt.Sprintf("lease-%d", f.opened)
			f.active[id] = true
			_ = json.NewEncoder(w).Encode(LeaseGrant{LeaseID: id, UserID: "user", AccountingID: "subscription-test", ExpiresAt: time.Now().Add(120 * time.Second), BudgetBytes: 1 << 20, RateVersion: 1})
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/leases/"), "/renew")
		if body.Closed && f.active[id] {
			delete(f.active, id)
			f.finalized++
		}
		_ = json.NewEncoder(w).Encode(LeaseGrant{LeaseID: id, UserID: "user", AccountingID: "subscription-test", ExpiresAt: time.Now().Add(120 * time.Second), BudgetBytes: 1 << 20, RateVersion: 1})
	}))
	t.Cleanup(srv.Close)
	a, err := NewAdmission(AdmissionConfig{URL: srv.URL, Secret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, f
}

func (f *activityAdmission) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.active), f.opened, f.finalized
}

type activityOutbound struct{ t *testing.T }

func (o activityOutbound) TCP(string) (net.Conn, error) {
	a, b := net.Pipe()
	o.t.Cleanup(func() { _ = b.Close() })
	return a, nil
}
func (o activityOutbound) UDP(string) (UDPConn, error) { return nil, fmt.Errorf("dial failed") }
func (o activityOutbound) CheckUDP(string) error       { return nil }

func TestHQIPIdleTransportDoesNotReserveSlots(t *testing.T) {
	a, f := activityFixture(t)
	l, _, err := a.open(context.Background(), randomConnectionID(), "credential", "192.0.2.1", func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer l.finish()
	if active, opened, _ := f.counts(); active != 0 || opened != 0 {
		t.Fatalf("idle authenticated transport reserved slots: active=%d opened=%d", active, opened)
	}
}

func TestHQIPLastBusinessFlowReleasesAndReacquires(t *testing.T) {
	a, f := activityFixture(t)
	var transportClosed atomic.Bool
	l, _, err := a.open(context.Background(), randomConnectionID(), "credential", "192.0.2.1", func() { transportClosed.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	defer l.finish()
	o := &leasedOutbound{base: activityOutbound{t}, lease: l}
	first, err := o.TCP("example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	second, err := o.TCP("example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	if active, _, _ := f.counts(); active != 1 {
		t.Fatal("one transport must share one active slot", active)
	}
	_ = first.Close()
	_ = first.Close()
	if active, _, _ := f.counts(); active != 1 {
		t.Fatal("closing one stream released a busy sibling", active)
	}
	_ = second.Close()
	if active, _, final := f.counts(); active != 0 || final != 1 {
		t.Fatalf("last stream retained slot: active=%d finals=%d", active, final)
	}
	if transportClosed.Load() {
		t.Fatal("normal idle release closed the reusable QUIC transport")
	}
	f.mu.Lock()
	f.deny = true
	f.mu.Unlock()
	if c, err := o.TCP("example.test:443"); err == nil {
		_ = c.Close()
		t.Fatal("released slot reused without new admission")
	}
	f.mu.Lock()
	f.deny = false
	f.mu.Unlock()
	third, err := o.TCP("example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = third.Close()
	if active, opened, final := f.counts(); active != 0 || opened != 2 || final != 2 {
		t.Fatal("reacquisition did not finish exactly once", active, opened, final)
	}
}
