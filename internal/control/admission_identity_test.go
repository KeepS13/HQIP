package control

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdmissionIdentityEndpointAuthenticatesNodeAndDoesNotAllocate(t *testing.T) {
	s, user, sub, node, _ := portalExportFixture(t)
	_, token, _, err := s.Store.NodeSecrets(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	password, err := s.Store.PortalCredential(user.ID, sub.ID, node.ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	h := s.PublicHandler()
	for _, auth := range []string{"", "wrong", token} {
		w := bearerRequest(h, http.MethodPost, "/api/v1/agents/admission/check", map[string]string{"password": password}, auth)
		if auth != token {
			if w.Code == 200 {
				t.Fatal("unauthenticated identity check")
			}
			continue
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"authenticated":true`) || strings.Contains(w.Body.String(), "leaseId") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var count int
	_ = s.Store.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=?", sub.ID).Scan(&count)
	if count != 0 {
		t.Fatal("check allocated a lease", count)
	}
}
