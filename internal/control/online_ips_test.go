package control

import (
	"net/http"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestOnlineIPAdmissionRequiresIngressAddress(t *testing.T) {
	s, user, subscription, node, _ := portalExportFixture(t)
	template, err := s.Store.SubscriptionTemplate(subscription.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	template.MaxConnections = 1
	if _, err = s.Store.SaveSubscriptionTemplate(template.ID, template, template.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SetOfflineAdmissionEnabled(node.ID, true); err != nil {
		t.Fatal(err)
	}
	_, token, _, err := s.Store.NodeSecrets(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	password, err := s.Store.PortalCredential(user.ID, subscription.ID, node.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{"/api/v1/agents/admission", "/api/v1/agents/offline-grants"} {
		body := map[string]string{"connectionId": strings.Repeat(string(rune('a'+i)), 64), "password": password, "billingProtocol": "vless", "billingCapability": "traffic-billing-v1", "quotaCapability": model.NodeQuotaCapability}
		if i == 0 {
			delete(body, "quotaCapability")
		}
		for _, ip := range []string{"", "198.51.100.1:443", "::ffff:0.0.0.0"} {
			body["clientIP"] = ip
			if w := bearerRequest(s.PublicHandler(), http.MethodPost, path, body, token); w.Code != 400 {
				t.Fatal("invalid IP admitted", path, ip, w.Code)
			}
		}
		body["clientIP"] = "::ffff:198.51.100.1"
		if w := bearerRequest(s.PublicHandler(), http.MethodPost, path, body, token); w.Code != 200 {
			t.Fatal("same IP rejected", path, w.Code, w.Body.String())
		}
		body["connectionId"] = strings.Repeat(string(rune('c'+i)), 64)
		body["clientIP"] = "198.51.100.2"
		if w := bearerRequest(s.PublicHandler(), http.MethodPost, path, body, token); w.Code == 200 {
			t.Fatal("new IP bypassed cap", path)
		}
	}
	e, err := s.Store.UserSubscription(user.ID, subscription.ID)
	if err != nil || e.Occupied != 1 {
		t.Fatal("HTTP paths disagree about IP count", e, err)
	}
}
