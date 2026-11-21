package agent

import (
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
	"strings"
	"testing"
	"time"
)

func TestOfflineBillingChildBudgetAndVersionSwitch(t *testing.T) {
	now := time.Now()
	parent := offlineTestGrant(now)
	parent.Billing = &billing.Grant{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, BudgetUnits: 1000000, EmergencyBytes: parent.EmergencyBudgetBytes}
	state := offlineState{Grant: parent, Online: true, Children: map[string]offlineChild{}}
	child, err := state.localGrant("off_child", now)
	if err != nil {
		t.Fatal(err)
	}
	first := billing.Segment{Version: 1, Upload: 2000, AppliedAt: now}
	if err = state.updateChild(child.ID, model.LeaseUsage{UploadBytes: 2000, Billing: []billing.Segment{first}}, now); err != nil {
		t.Fatal(err)
	}
	state.Grant.Billing.Policy = billing.Policy{Version: 2, Multiplier: 200, Protocol: "hysteria2"}
	if _, err = state.localGrant(child.ID, now); err != nil {
		t.Fatal(err)
	}
	second := billing.Segment{Version: 2, Upload: 2000, AppliedAt: now.Add(time.Second)}
	if err = state.updateChild(child.ID, model.LeaseUsage{UploadBytes: 4000, Closed: true, Billing: []billing.Segment{first, second}}, now); err != nil {
		t.Fatal(err)
	}
	if state.Children[child.ID].Normal != 700000 {
		t.Fatal("unused charge budget not released")
	}
	usage, err := state.usage(now)
	if err != nil || usage.UploadBytes != 4000 || len(usage.Billing) != 2 {
		t.Fatalf("invalid parent report %+v %v", usage, err)
	}
}

func TestRealityBillingIdentityRejectsForgedOrExpiredTicket(t *testing.T) {
	now := time.Now()
	agent := &Agent{cfg: model.AgentConfig{NodeID: "node-a", Token: strings.Repeat("x", 32)}, now: func() time.Time { return now }}
	password := strings.Repeat("p", 43)
	ticket, err := agent.realityBillingPassword(password, "198.51.100.1")
	if err != nil {
		t.Fatal(err)
	}
	resolved, protocol, ip, err := agent.billingIdentity(ticket, billing.Capability, "127.0.0.1")
	if err != nil || resolved != password || protocol != "vless" || ip != "198.51.100.1" {
		t.Fatalf("invalid trusted marker %v", err)
	}
	if _, _, _, err = agent.billingIdentity(ticket+"a", billing.Capability, "127.0.0.1"); err == nil {
		t.Fatal("forged marker accepted")
	}
	if _, _, _, err = agent.billingIdentity(ticket, "", "127.0.0.1"); err == nil {
		t.Fatal("legacy runtime accepted marked credential")
	}
	now = now.Add(time.Minute)
	if _, _, _, err = agent.billingIdentity(ticket, billing.Capability, "127.0.0.1"); err == nil {
		t.Fatal("expired marker accepted")
	}
}

func TestOfflineBillingConfirmedSegmentsStayOutOfReports(t *testing.T) {
	now := time.Now()
	parent := offlineTestGrant(now)
	parent.Billing = &billing.Grant{Policy: billing.Policy{Version: 1, Multiplier: 150, Protocol: "hysteria2"}, BudgetUnits: 1 << 30, EmergencyBytes: parent.EmergencyBudgetBytes}
	state := offlineState{Grant: parent, Online: true, Children: map[string]offlineChild{}}
	for version := int64(1); version <= 300; version++ {
		state.Grant.Billing.Version = version
		child, err := state.localGrant("off_child", now)
		if err != nil {
			t.Fatal(err)
		}
		segment := billing.Segment{Version: version, Upload: 1, AppliedAt: now.Add(time.Duration(version) * time.Millisecond), ValidUntil: now.Add(time.Minute)}
		if err = state.updateChild(child.ID, model.LeaseUsage{UploadBytes: version, Billing: []billing.Segment{segment}}, now); err != nil {
			t.Fatal(err)
		}
		usage, err := state.usage(now)
		if err != nil || usage.UploadBytes != version || len(usage.Billing) != 1 {
			t.Fatalf("unbounded/repeated segment report: %d %+v %v", version, usage, err)
		}
		state.Grant.Billing.Acknowledged = usage.Billing
		if err = state.rememberBilling(); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := state.usage(now)
	if err != nil || len(usage.Billing) != 0 || usage.UploadBytes != 300 {
		t.Fatal("confirmed history lost or resent", err)
	}
}
