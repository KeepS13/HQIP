package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestClientNodeCatalogContainsOnlyValidSubscriptionEndpoints(t *testing.T) {
	s, now, user, _, node, password := portalFixture(t, 2, nil)
	foreign := newNode(t, s, "foreign-node")
	retired := newNode(t, s, "retired-node")
	if _, err := s.DB.Exec("UPDATE nodes SET lifecycle='deleted' WHERE id=?", retired.ID); err != nil {
		t.Fatal(err)
	}
	subscriptions := append([]model.UserSubscription{}, user.Subscriptions...)
	subscriptions = append(subscriptions, subscriptions[0])
	subscriptions[1].ID = "another-instance"
	subscriptions[1].NodeIDs = append(subscriptions[1].NodeIDs, retired.ID, "missing")
	for _, status := range []string{"exhausted", "removed", "expired", "disabled"} {
		subscriptions = append(subscriptions, model.UserSubscription{ID: status, Status: status, ExpiresAt: now.Add(time.Hour), NodeIDs: []string{foreign.ID}})
	}
	subscriptions = append(subscriptions, model.UserSubscription{ID: "past-expiry", Status: "active", ExpiresAt: now.Add(-time.Second), NodeIDs: []string{foreign.ID}})
	catalog, err := s.ClientNodeCatalog(subscriptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || catalog[0].ID != node.ID || !catalog[0].Ready {
		t.Fatalf("catalog included inaccessible or duplicate nodes: %+v", catalog)
	}
	raw, _ := json.Marshal(catalog)
	for _, secret := range []string{password, "password", "sshHost", "foreign-node"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("catalog leaked restricted data", secret)
		}
	}
}
