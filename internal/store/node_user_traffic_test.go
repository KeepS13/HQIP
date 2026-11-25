package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNodeUserTrafficMergesSubscriptionsAndRetainsRawHistory(t *testing.T) {
	s, now, user, template, node, password := portalFixture(t, 3, nil)
	ctx := context.Background()
	empty, err := s.NodeUserTraffic(ctx, node.ID, 0, 20)
	if err != nil || empty.Items == nil || empty.TotalUsers != 0 {
		t.Fatalf("unconnected allocation must not be a usage record: %+v %v", empty, err)
	}
	second := grantAnother(t, s, user.ID, template, "second-ranking")
	a := acquire(t, s, node, password, "a")
	b := acquire(t, s, node, subscriptionPassword(t, s, user.ID, second.ID, node.ID), "b")
	other, err := s.RegisterPortal("other-ranking@example.com", "ranking-test-password")
	if err != nil {
		t.Fatal(err)
	}
	otherSub := grantAnother(t, s, other.ID, template, "other-ranking")
	c := acquire(t, s, node, subscriptionPassword(t, s, other.ID, otherSub.ID, node.ID), "c")
	missing, err := s.RegisterPortal("missing-ranking@example.com", "ranking-test-password")
	if err != nil {
		t.Fatal(err)
	}
	missingSub := grantAnother(t, s, missing.ID, template, "missing-ranking")
	_ = acquire(t, s, node, subscriptionPassword(t, s, missing.ID, missingSub.ID, node.ID), "d")
	desired, err := s.Desired(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	report := model.AgentReport{Revision: desired.Revision, CoreHealthy: true, Daily: []model.UsageBucket{
		{DeviceID: a.AccountingID, Start: DayStart(*now), Upload: 100, Download: 300},
		{DeviceID: b.AccountingID, Start: DayStart(*now), Upload: 200, Download: 400},
		{DeviceID: c.AccountingID, Start: DayStart(*now), Upload: 50, Download: 550},
		{DeviceID: ProbeID(node.ID), Start: DayStart(*now), Upload: 99999, Download: 99999},
	}, Minutes: []model.UsageBucket{{DeviceID: a.AccountingID, Start: now.Unix() / 60 * 60, Upload: 100, Download: 300}}}
	for i := 0; i < 2; i++ {
		if err = s.Report(node.ID, report); err != nil {
			t.Fatal(err)
		}
	}
	// Billing, stale authorization status and other-node counters must not
	// rewrite or inflate this node's raw historical attribution.
	audienceExec(t, s, "UPDATE portal_usage SET upload=1500,download=3000 WHERE subscription_id=?", second.ID)
	audienceExec(t, s, "UPDATE portal_subscriptions SET revoked=1,expires=? WHERE user_id=?", now.Unix()-1, user.ID)
	audienceExec(t, s, "UPDATE portal_users SET enabled=0 WHERE id=?", user.ID)
	otherNode := newNode(t, s, "ranking-other-node")
	audienceExec(t, s, "INSERT INTO node_accounting_identities(node_id,accounting_id,subject_type,subject_id) VALUES(?,?,'subscription',?)", otherNode.ID, a.AccountingID, user.Subscriptions[0].ID)
	audienceExec(t, s, "INSERT INTO lifetime(node_id,device_id,upload,download) VALUES(?,?,90000,90000)", otherNode.ID, a.AccountingID)
	audienceExec(t, s, "INSERT INTO node_accounting_identities(node_id,accounting_id,subject_type,subject_id) VALUES(?,'legacy','historical','')", node.ID)
	for _, identity := range []string{"legacy", "unmapped"} {
		audienceExec(t, s, "INSERT INTO lifetime(node_id,device_id,upload,download) VALUES(?,?,99999,99999)", node.ID, identity)
	}
	got, err := s.NodeUserTraffic(ctx, node.ID, 0, 20)
	if err != nil || got.TotalUsers != 3 || len(got.Items) != 3 {
		t.Fatalf("page: %+v %v", got, err)
	}
	first := got.Items[0]
	if first.UserID != user.ID || first.Email != user.Email || first.Enabled || !first.HasData || first.Upload != 300 || first.Download != 700 || first.Total != 1000 {
		t.Fatalf("merge/replay/raw/history invariant: %+v", first)
	}
	if got.Items[1].UserID != other.ID || got.Items[1].Total != 600 || got.Items[2].UserID != missing.ID || got.Items[2].HasData {
		t.Fatalf("descending or absent-data invariant: %+v", got.Items)
	}
	isolated, err := s.NodeUserTraffic(ctx, otherNode.ID, 0, 20)
	if err != nil || isolated.TotalUsers != 1 || isolated.Items[0].Total != 180000 {
		t.Fatalf("node isolation: %+v %v", isolated, err)
	}
}

func TestNodeUserTrafficPaginationTiesZeroAndBounds(t *testing.T) {
	s, _, _, template, node, _ := portalFixture(t, 1, nil)
	want := make([]model.NodeUserTraffic, 43)
	for i := range want {
		id := fmt.Sprintf("ranking-user-%02d", i)
		up := int64(i%4) * 1000
		audienceExec(t, s, "INSERT INTO portal_users(id,email,password,created_at) VALUES(?,?,?,0)", id, id+"@example.com", "test-fixture")
		audienceExec(t, s, "INSERT INTO portal_subscriptions(id,user_id,template_id,created_at,expires,grant_key) VALUES(?,?,?,0,0,?)", id, id, template.ID, id)
		audienceExec(t, s, "INSERT INTO node_accounting_identities(node_id,accounting_id,subject_type,subject_id) VALUES(?,?,'subscription',?)", node.ID, id, id)
		audienceExec(t, s, "INSERT INTO lifetime(node_id,device_id,upload,download) VALUES(?,?,?,0)", node.ID, id, up)
		want[i] = model.NodeUserTraffic{UserID: id, Total: up}
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].Total != want[j].Total {
			return want[i].Total > want[j].Total
		}
		return want[i].UserID < want[j].UserID
	})
	for offset := 0; offset < 60; offset += 20 {
		page, err := s.NodeUserTraffic(context.Background(), node.ID, offset, 20)
		if err != nil || page.TotalUsers != len(want) || page.Offset != offset || page.Limit != 20 || page.NodeID != node.ID || !page.GeneratedAt.Equal(s.Now()) {
			t.Fatalf("page metadata: %+v %v", page, err)
		}
		if len(page.Items) != min(20, len(want)-offset) {
			t.Fatalf("page size: %+v", page)
		}
		for i, item := range page.Items {
			if item.UserID != want[offset+i].UserID || item.Total != want[offset+i].Total || !item.HasData {
				t.Fatalf("stable tie/zero at %d: %+v", offset+i, item)
			}
		}
	}
	page, err := s.NodeUserTraffic(context.Background(), node.ID, 100, 20)
	if err != nil || page.TotalUsers != len(want) || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("out of range: %+v %v", page, err)
	}
	for _, bounds := range [][2]int{{-1, 20}, {1000001, 20}, {0, 0}, {0, 101}} {
		if _, err = s.NodeUserTraffic(context.Background(), node.ID, bounds[0], bounds[1]); !errors.Is(err, ErrNodeUserTrafficPage) {
			t.Fatalf("invalid page accepted: %v", err)
		}
	}
	if _, err = s.NodeUserTraffic(context.Background(), "missing", 0, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.NodeUserTraffic(ctx, node.ID, 0, 20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %v", err)
	}
}

func TestNodeUserTrafficRejectsOverflow(t *testing.T) {
	s, _, _, _, node, password := portalFixture(t, 1, nil)
	lease := acquire(t, s, node, password, "a")
	audienceExec(t, s, "INSERT INTO lifetime(node_id,device_id,upload,download) VALUES(?,?,?,1)", node.ID, lease.AccountingID, int64(math.MaxInt64))
	if _, err := s.NodeUserTraffic(context.Background(), node.ID, 0, 20); err == nil {
		t.Fatal("overflow produced a valid ranking")
	}
}
