package server

import (
	"github.com/apernet/hysteria/core/v2/billing"
	"testing"
	"time"
)

func TestBillingConsumptionPreservesRawAndBoundsCost(t *testing.T) {
	lease := &connectionLease{grant: LeaseGrant{Billing: &billing.Grant{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, BudgetUnits: 3000}}, billingSegments: []billing.Segment{{Version: 1, AppliedAt: time.Now()}}}
	if !lease.billingAllows(20) || lease.billingAllows(21) {
		t.Fatal("incorrect weighted budget")
	}
	lease.chargeBilling(true, 10)
	if lease.billingNormal != 1500 || lease.billingSegments[0].Upload != 10 || lease.usedUp != 0 {
		t.Fatal("raw accounting changed")
	}
	lease.grant.Billing.Multiplier = 200
	if !lease.billingAllows(7) || lease.billingAllows(8) {
		t.Fatal("new multiplier bypassed reserved cap")
	}
}

func TestBillingEmergencyIsLimitedInRawBytes(t *testing.T) {
	lease := &connectionLease{grant: LeaseGrant{Billing: &billing.Grant{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, BudgetUnits: 300, EmergencyBytes: 3}}, billingSegments: []billing.Segment{{Version: 1, AppliedAt: time.Now()}}}
	if !lease.billingAllows(5) || lease.billingAllows(6) {
		t.Fatal("emergency cap changed by multiplier")
	}
	lease.chargeBilling(false, 5)
	if lease.billingNormal != 300 || lease.billingEmergency != 3 || lease.billingSegments[0].EmergencyDownload != 3 {
		t.Fatal("emergency classified incorrectly")
	}
}

func TestBillingLostRenewalKeepsOldBytesUntilActualAcceptance(t *testing.T) {
	now := time.Now()
	old := LeaseGrant{LeaseID: "lease", UserID: "user", AccountingID: "subscription", ExpiresAt: now.Add(time.Minute), BudgetBytes: billing.MaxRawBytes, RateVersion: 1, BandwidthBytesPerSecond: 100000, Billing: &billing.Grant{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, BudgetUnits: 10000000}}
	lease := &connectionLease{wake: make(chan struct{}, 1), rateWake: make(chan struct{})}
	if err := lease.acceptGrant(old, now, true); err != nil {
		t.Fatal(err)
	}
	lease.chargeBilling(true, 1000)
	lease.usedUp += 1000
	// The controller may have committed a renewal whose response was lost.
	// Nothing changes locally until a later response is accepted.
	ack := lease.billingSegments[0]
	lease.chargeBilling(true, 400)
	lease.usedUp += 400
	next := old
	next.ConsumedBytes = 1000
	next.Billing = &billing.Grant{Policy: billing.Policy{Version: 2, Multiplier: 200, Protocol: "hysteria2"}, BudgetUnits: 10000000, Acknowledged: []billing.Segment{ack}}
	if err := lease.acceptGrant(next, now, false); err != nil {
		t.Fatal(err)
	}
	lease.chargeBilling(false, 600)
	lease.usedDown += 600
	if len(lease.billingSegments) != 2 || lease.billingSegments[0].Upload != 1400 || lease.billingSegments[1].Download != 600 || lease.billingNormal != 330000 || lease.grant.BandwidthBytesPerSecond != 100000 {
		t.Fatal("response loss repriced old bytes or changed forwarding rate")
	}
}
