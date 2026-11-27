package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFixedAccountLinkExportsEmptyThenPurchasesWithoutRecopy(t *testing.T) {
	s, _ := newServer(t)
	user, cookie, csrf := portalLoginFixture(t, s, "fixed-account@example.com")
	if err := s.Store.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	h := s.PublicHandler()
	w := portalRequest(h, "POST", "/api/v1/user/import-link", map[string]any{}, cookie, csrf, s.opts.PublicURL)
	var result struct {
		URL string `json:"url"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.URL == "" {
		t.Fatal("no-plan account cannot create link", w.Code)
	}
	for _, format := range []string{"clash", "v2ray", "singbox"} {
		w = request(h, "GET", result.URL+"?format="+format, nil, "", "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "password") {
			t.Fatal("empty export granted credential", format, w.Code)
		}
	}
	node := allocationNode(t, s)
	sub := subscriptionFixture(t, s, user, []string{node.ID}, node.ID, 2)
	w = request(h, "GET", result.URL+"?format=v2ray", nil, "", "")
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatal("purchase not loaded through original URL", w.Code)
	}
	if _, err := s.Store.PortalSubscription(user.ID, sub.ID, "rotate"); err != nil {
		t.Fatal(err)
	}
	w = request(h, "GET", result.URL+"?format=v2ray", nil, "", "")
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatal("single rotation broke default URL", w.Code)
	}
	if _, err := s.Store.DB.Exec("UPDATE portal_subscriptions SET expires=? WHERE id=?", s.Store.Now().Unix()-1, sub.ID); err != nil {
		t.Fatal(err)
	}
	w = request(h, "GET", result.URL+"?format=v2ray", nil, "", "")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("expired membership not empty", w.Code)
	}
}
