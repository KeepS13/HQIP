package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestOfflineRecoveryHTTPRequiresNodeAndPreservesReceipt(t *testing.T) {
	s, user, subscription, node, _ := portalExportFixture(t)
	if err := s.Store.SetOfflineAdmissionEnabled(node.ID, true); err != nil {
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
	connection := strings.Repeat("a", 64)
	g, err := s.Store.AcquireNodeQuotaGrant(node.ID, connection, password, "hysteria2", "198.51.100.1")
	if err != nil {
		t.Fatal(err)
	}
	handler := s.PublicHandler()
	path := "/api/v1/agents/offline-grants/recover"
	body := map[string]string{"connectionId": connection}
	if w := bearerRequest(handler, http.MethodPost, path, body, "invalid-token"); w.Code == 200 {
		t.Fatal("unauthenticated recovery returned a receipt")
	}
	w := bearerRequest(handler, http.MethodPost, path, body, token)
	var recovered model.OfflineGrant
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &recovered) != nil || recovered.ID != g.ID || recovered.ReportVersion != 0 {
		t.Fatal("node could not recover its committed admission", w.Code, w.Body.String())
	}
	w = bearerRequest(handler, http.MethodPost, "/api/v1/agents/offline-grants/"+g.ID+"/report", model.OfflineUsage{Version: 1, Closed: true}, token)
	if w.Code != 200 {
		t.Fatal("recovery could not settle through normal report API", w.Code, w.Body.String())
	}
	e, err := s.Store.UserSubscription(user.ID, subscription.ID)
	if err != nil || e.Occupied != 0 || e.UsedBytes != 0 {
		t.Fatal("unused HTTP admission still occupies a slot", e, err)
	}
}
