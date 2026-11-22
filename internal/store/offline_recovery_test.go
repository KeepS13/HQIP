package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestOfflineRecoveryReturnsOnlyCommittedNodeReceipt(t *testing.T) {
	quota := int64(200_000_000)
	s, _, node, password := offlineFixture(t, 1, &quota)
	connection := strings.Repeat("a", 64)
	if _, err := s.RecoverOfflineGrant(node.ID, connection); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown request should not create authority", err)
	}
	g, err := s.AcquireNodeQuotaGrant(node.ID, connection, password, "hysteria2", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecoverOfflineGrant("other-node", connection); !errors.Is(err, ErrNotFound) {
		t.Fatal("another node read a receipt", err)
	}
	for i := 0; i < 2; i++ {
		recovered, err := s.RecoverOfflineGrant(node.ID, connection)
		if err != nil || !reflect.DeepEqual(recovered, g) {
			t.Fatal("recovery changed the committed receipt", recovered, g, err)
		}
	}
	e, err := s.UserSubscription(g.UserID, g.SubscriptionID)
	if err != nil || e.Occupied != 1 || e.UsedBytes != 0 || e.ReservedBytes != 0 {
		t.Fatal("lookup changed accounting or slot ownership", e, err)
	}
	final := model.OfflineUsage{Version: 1, Closed: true}
	for i := 0; i < 2; i++ {
		if _, err = s.ReportOfflineGrant(node.ID, g.ID, final); err != nil {
			t.Fatal(err)
		}
	}
	e, err = s.UserSubscription(g.UserID, g.SubscriptionID)
	if err != nil || e.Occupied != 0 || e.UsedBytes != 0 || e.ReservedBytes != 0 {
		t.Fatal("recovered unused grant did not release exactly once", e, err)
	}
}

func TestOfflineRecoveryWorksAfterCredentialRemovalAndExpiry(t *testing.T) {
	quota := int64(200_000_000)
	s, now, node, password := offlineFixture(t, 1, &quota)
	connection := strings.Repeat("a", 64)
	g, err := s.AcquireNodeQuotaGrant(node.ID, connection, password, "hysteria2", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("DELETE FROM portal_credentials WHERE node_id=? AND subscription_id=?", node.ID, g.SubscriptionID); err != nil {
		t.Fatal(err)
	}
	*now = g.OfflineUntil.Add(time.Second)
	recovered, err := s.RecoverOfflineGrant(node.ID, connection)
	if err != nil || recovered.ID != g.ID || recovered.ReportVersion != 0 {
		t.Fatal("retired credentials prevented lost admission settlement", recovered, err)
	}
	closed, err := s.ReportOfflineGrant(node.ID, g.ID, model.OfflineUsage{Version: 1, Closed: true})
	if err != nil || !closed.Closed || closed.UploadBytes != 0 || closed.DownloadBytes != 0 {
		t.Fatal("expired receipt could not settle", closed, err)
	}
}
