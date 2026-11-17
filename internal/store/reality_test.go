package store

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func realityFixture(t *testing.T, s *Store, node string) model.RealityEndpoint {
	t.Helper()
	endpoint := model.RealityEndpoint{Port: 8443, ServerName: "www.cloudflare.com", PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortID: "0123456789abcdef"}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = recordReality(tx, node, model.AgentReport{Reality: &endpoint, Capabilities: []string{model.RealityCapability}}, true); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func TestRealityScopeQuotaRotationAndCatalog(t *testing.T) {
	quota := int64(20 << 20)
	s, _, user, _, node, password := portalFixture(t, 1, &quota)
	realityFixture(t, s, node.ID)
	sub := user.Subscriptions[0]
	profile, entitlement, err := s.PortalClientProfile(user.ID, "subscription", sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Nodes) != 2 || len(entitlement.NodeIDs) != 1 {
		t.Fatalf("physical allocation was duplicated: %d/%d", len(profile.Nodes), len(entitlement.NodeIDs))
	}
	hy, vless := profile.Nodes[0], profile.Nodes[1]
	if hy.ID != node.ID || hy.Password != password || vless.ID != node.ID+":vless" || vless.Protocol != "vless" || vless.Password != model.RealityUUID(password) || vless.NodeID != node.ID {
		t.Fatal("invalid dual protocol profile")
	}
	if vless.Server != hy.Server || vless.Reality == nil || vless.Port != 8443 || vless.OfflineAdmissionVersion != 0 {
		t.Fatal("wrong Reality endpoint or offline promise")
	}
	resolved, err := s.ResolveRealityCredential(context.Background(), node.ID, vless.Password)
	if err != nil || resolved != password {
		t.Fatal("alias did not resolve inside its own node", err)
	}
	if _, err = s.ResolveRealityCredential(context.Background(), "another-node", vless.Password); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("cross-node credential exposure", err)
	}
	lease := acquire(t, s, node, password, "a")
	if _, err = s.AcquireLease(node.ID, strings.Repeat("b", 32), resolved); !errors.Is(err, ErrSlots) {
		t.Fatal("second protocol bypassed shared slot", err)
	}
	renew(t, s, lease, 200, 300, true)
	updated, _ := s.UserSubscription(user.ID, sub.ID)
	if updated.UsedBytes != 500 {
		t.Fatal("two protocols lost shared accounting")
	}
	catalog, err := s.ClientNodeCatalog(user.Subscriptions)
	if err != nil || len(catalog) != 2 || catalog[1].NodeID != node.ID {
		t.Fatal("missing public protocol catalog", err)
	}
	if _, err = s.PortalSubscription(user.ID, sub.ID, "rotate"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveRealityCredential(context.Background(), node.ID, vless.Password); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old Reality UUID survived rotation", err)
	}
	profile, _, err = s.PortalClientProfile(user.ID, "subscription", sub.ID)
	if err != nil || len(profile.Nodes) != 2 || profile.Nodes[1].Password == vless.Password {
		t.Fatal("rotation did not replace both credentials", err)
	}
}

func TestRealityReportFailClosedAndDowngrade(t *testing.T) {
	s, _ := fixture(t)
	node := newNode(t, s, "reality-report")
	endpoint := realityFixture(t, s, node.ID)
	for _, report := range []model.AgentReport{{}, {Reality: &endpoint}} {
		tx, _ := s.DB.Begin()
		if err := recordReality(tx, node.ID, report, true); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got, err := s.RealityEndpoint(node.ID); err != nil || got != nil {
			t.Fatal("old/incapable Agent left a visible endpoint", err)
		}
	}
	endpoint.ShortID = "invalid"
	tx, _ := s.DB.Begin()
	defer tx.Rollback()
	if err := recordReality(tx, node.ID, model.AgentReport{Reality: &endpoint, Capabilities: []string{model.RealityCapability}}, true); err == nil {
		t.Fatal("malformed public parameters accepted")
	}
}
