package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func recoveryAgent(t *testing.T, g model.OfflineGrant, handler http.HandlerFunc) (*Agent, offlineRequest) {
	t.Helper()
	central := httptest.NewServer(handler)
	t.Cleanup(central.Close)
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return g.AuthorizedAt }
	a.cache.PortalEnabled, a.cache.OfflineAdmissionVersion, a.cache.NodeQuotaVersion = true, 1, 1
	r := offlineRequest{Hash: g.Billing.Protocol + ":" + g.CredentialHash + ":" + g.ClientIP, ID: strings.Repeat("a", 64)}
	if _, _, err := a.ledger.trackOfflineRequest(r.Hash, r.ID); err != nil {
		t.Fatal(err)
	}
	return a, r
}

func TestOfflineRecoveryPreservesSavedChildren(t *testing.T) {
	for _, throughAdmission := range []bool{false, true} {
		t.Run(map[bool]string{false: "background", true: "client_retry"}[throughAdmission], func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			s := quotaTestState(now, "saved-parent", "hysteria2", 100_000_000)
			s.Grant.ClientIP, s.Grant.ConnectionSlotsVersion = "198.51.100.1", model.ConnectionSlotsVersion
			a, pending := recoveryAgent(t, s.Grant, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(s.Grant)
			})
			if _, err := a.localOfflineGrant(&s, "off_original", now); err != nil {
				t.Fatal(err)
			}
			if err := a.ledger.saveOffline(s); err != nil {
				t.Fatal(err)
			}
			before, _, err := a.ledger.offlineChild("off_original")
			if err != nil {
				t.Fatal(err)
			}
			if throughAdmission {
				w := admissionRequest(a, "/admission", `{"clientIP":"198.51.100.1","billingCapability":"traffic-billing-v1","connectionId":"`+strings.Repeat("b", 64)+`","password":"`+firstPassword+`"}`, "127.0.0.1:3000", testSecret)
				if w.Code == 200 {
					t.Fatal("retry reused a busy accounting parent")
				}
			} else if err := a.recoverOfflineRequest(context.Background(), pending, map[string]offlineState{s.Grant.ID: s}); err != nil {
				t.Fatal(err)
			}
			states, err := a.ledger.offlineStates()
			if err != nil || len(states) != 1 || !reflect.DeepEqual(states[0].Children, before.Children) || states[0].Denied || states[0].Grant.Closed {
				t.Fatal("recovery overwrote a durable live child", states, err)
			}
			requests, err := a.ledger.offlineRequests("")
			if err != nil || len(requests) != 0 {
				t.Fatal("saved parent did not retire its stale request key", requests, err)
			}
		})
	}
}

func TestOfflineRecoveryRejectsLostLedgerAndIdentityMismatch(t *testing.T) {
	for _, fault := range []string{"used", "node", "credential", "protocol", "ip"} {
		t.Run(fault, func(t *testing.T) {
			g := quotaTestState(time.Now(), "missing-parent", "hysteria2", 100_000_000).Grant
			g.ClientIP, g.ConnectionSlotsVersion = "2001:db8::1", model.ConnectionSlotsVersion
			bad := g
			switch fault {
			case "used":
				bad.ReportVersion, bad.DownloadBytes = 1, 100
			case "node":
				bad.NodeID = "other-node"
			case "credential":
				bad.CredentialHash = "other-credential"
			case "protocol":
				copy := *bad.Billing
				copy.Protocol = "vless"
				bad.Billing = &copy
			case "ip":
				bad.ClientIP = "2001:db8::2"
			}
			a, pending := recoveryAgent(t, g, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(bad) })
			if err := a.recoverOfflineRequest(context.Background(), pending, nil); err == nil {
				t.Fatal("invalid recovery receipt accepted")
			}
			requests, _ := a.ledger.offlineRequests("")
			states, _ := a.ledger.offlineStates()
			if len(requests) != 1 || len(states) != 0 {
				t.Fatal("recovery discarded unknown usage or identity", requests, states)
			}
		})
	}
}

func TestOfflineRecoveryMissingReceiptIsRetriedAfterCommit(t *testing.T) {
	g := quotaTestState(time.Now(), "later-commit", "hysteria2", 100_000_000).Grant
	g.ClientIP, g.ConnectionSlotsVersion = "2001:db8::1", model.ConnectionSlotsVersion
	var committed atomic.Bool
	a, pending := recoveryAgent(t, g, func(w http.ResponseWriter, r *http.Request) {
		if !committed.Load() {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(g)
	})
	for i := 0; i < 2; i++ {
		if err := a.recoverOfflineRequest(context.Background(), pending, map[string]offlineState{}); err != nil {
			t.Fatal(err)
		}
		requests, _ := a.ledger.offlineRequests("")
		if len(requests) != 1-i {
			t.Fatal("missing receipt lost its durable key", requests)
		}
		committed.Store(true)
	}
	states, _ := a.ledger.offlineStates()
	if len(states) != 1 || !states[0].Denied || len(states[0].Children) != 0 {
		t.Fatal("recovery created forwarding permission", states)
	}
}

func TestOfflineRecoveryExpiredInitialReceiptCanSettle(t *testing.T) {
	now := time.Now()
	g := quotaTestState(now.Add(-4*24*time.Hour), "expired-admission", "hysteria2", 100_000_000).Grant
	g.ClientIP, g.ConnectionSlotsVersion = "198.51.100.1", model.ConnectionSlotsVersion
	s, err := initialOfflineState(g, g.NodeID, g.CredentialHash, "hysteria2", g.ClientIP, now)
	if err != nil || !s.Denied {
		t.Fatal("expired receipt cannot enter settlement", s, err)
	}
	u, err := s.usage(now)
	if err != nil || !u.Closed || u.Active {
		t.Fatal("expired receipt retained a slot", u, err)
	}
	if _, err = s.localGrant("unsafe", now); err == nil {
		t.Fatal("expired receipt authorized forwarding")
	}
}
