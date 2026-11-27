package control

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/store"
)

func TestTemplateApplyAdminBoundaryAndUpdatedExport(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	u, cookie, _ := portalLoginFixture(t, s, "apply-api@example.com")
	a, b := allocationNode(t, s), allocationNode(t, s)
	sub := subscriptionFixture(t, s, u, []string{a.ID}, a.ID, 2)
	if err := s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	p, err := s.Store.SubscriptionTemplate(sub.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	p.NodeIDs = append(p.NodeIDs, b.ID)
	p.NodesPerSubscription = 2
	p, err = s.Store.SaveSubscriptionTemplate(p.ID, p, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/subscription-templates/" + p.ID + "/apply"
	body := map[string]any{"expectedRevision": p.Revision}
	for _, auth := range []struct {
		cookie, csrf string
		status       int
	}{{"", "", 401}, {cookie, "", 401}, {admin, "", 403}} {
		if w := request(h, "POST", path, body, auth.cookie, auth.csrf); w.Code != auth.status {
			t.Fatal("auth boundary", w.Code)
		}
	}
	if w := request(h, "POST", path, map[string]any{"expectedRevision": p.Revision - 1}, admin, csrf); w.Code != 409 {
		t.Fatal("stale apply", w.Code)
	}
	link, err := s.Store.PortalAllSubscriptions(u.ID, "create")
	if err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", path, body, admin, csrf)
	var applied store.TemplateApplyResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &applied) != nil || applied.UpdatedSubscriptions != 1 {
		t.Fatal("apply", w.Code, w.Body.String())
	}
	w = request(s.PublicHandler(), "GET", link+"?format=singbox", nil, "", "")
	var config struct{ Outbounds []struct{ Password string } }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &config) != nil {
		t.Fatal("export", w.Code)
	}
	count := 0
	for _, out := range config.Outbounds {
		if out.Password != "" {
			count++
		}
	}
	if count != 2 {
		t.Fatal("new nodes not in original subscription URL", count)
	}
}
