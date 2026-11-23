package nodepolicy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIdentityCheckCannotAllocateOrFallBack(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"identity", `{"authenticated":true,"accountingId":"subscription-test"}`, 200, true},
		{"legacy", `{"legacy":true,"accountingId":"legacy-test"}`, 200, true},
		{"old-agent", `{}`, 404, false},
		{"missing-account", `{"authenticated":true}`, 200, false},
		{"unconfirmed", `{"accountingId":"subscription-test"}`, 200, false},
		{"unexpected-lease", `{"authenticated":true,"accountingId":"subscription-test","leaseId":"lease"}`, 200, false},
		{"truncated-json", `{"authenticated":true`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/admission/check" || r.Method != http.MethodPost {
					t.Error("check attempted forwarding admission", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			a, err := NewAdmission(AdmissionConfig{URL: srv.URL, Secret: strings.Repeat("s", 32)})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			id, err := a.Check(context.Background(), NewConnectionID(), "credential", "192.0.2.1")
			if (err == nil) != tc.want || calls.Load() != 1 || (tc.want && id == "") {
				t.Fatal("invalid identity result or fallback", id, err, calls.Load())
			}
		})
	}
}

func TestIdentityReceiptNeverAuthorizesForwarding(t *testing.T) {
	var legacy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := LeaseGrant{Authenticated: true, LeaseID: "lease", UserID: "user", AccountingID: "subscription-test", ExpiresAt: time.Now().Add(time.Minute), BudgetBytes: 1 << 20}
		if legacy.Load() {
			g.Legacy = true
			g.LeaseID = ""
		}
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer srv.Close()
	a, err := NewAdmission(AdmissionConfig{URL: srv.URL, Secret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, compatibility := range []bool{false, true} {
		legacy.Store(compatibility)
		l, _, err := a.Open(context.Background(), NewConnectionID(), "credential", "192.0.2.1", func() {})
		if err == nil {
			l.Finish()
			t.Fatal("identity receipt authorized traffic", compatibility)
		}
	}
}
