package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestAlertReadRoutesRequireAdminAndCSRF(t *testing.T) {
	s, h := newServer(t)
	if err := s.Store.EvaluateAlerts([]model.NotificationCondition{{Key: "read-route", Category: "system", Kind: "node.health", Title: "节点异常", Stage: 1}}); err != nil {
		t.Fatal(err)
	}
	page, err := s.Store.ListAlerts(1, 20, "", "", "", "", false)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("fixture alerts: %+v %v", page, err)
	}
	a := page.Items[0]
	path := "/api/v1/alerts/" + a.ID + "/read"
	body := model.AlertReadUpdate{ExpectedRevision: a.Revision}
	for _, tc := range []struct{ method, path string }{{"GET", "/api/v1/alerts/unread"}, {"POST", path}} {
		if w := request(h, tc.method, tc.path, body, "", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", tc.path, w.Code)
		}
		if w := request(s.PublicHandler(), tc.method, tc.path, body, "", ""); w.Code != http.StatusNotFound {
			t.Fatalf("public route %s: %d", tc.path, w.Code)
		}
	}
	cookie, csrf := loginAdmin(t, s, h)
	if w := request(h, "POST", path, body, cookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF accepted: %d", w.Code)
	}
	readCount := func(want int) {
		t.Helper()
		w := request(h, "GET", "/api/v1/alerts/unread", nil, cookie, "")
		var count model.AlertUnreadSummary
		if err := json.Unmarshal(w.Body.Bytes(), &count); w.Code != 200 || err != nil || count.Unread != want {
			t.Fatalf("unread want %d: code=%d body=%s err=%v", want, w.Code, w.Body.String(), err)
		}
	}
	readCount(1)
	if w := request(h, "POST", path, model.AlertReadUpdate{}, cookie, csrf); w.Code != http.StatusBadRequest {
		t.Fatalf("missing revision: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "POST", path, model.AlertReadUpdate{ExpectedRevision: a.Revision + 1}, cookie, csrf); w.Code != http.StatusConflict {
		t.Fatalf("stale revision: %d %s", w.Code, w.Body.String())
	}
	readCount(1)
	if w := request(h, "POST", "/api/v1/alerts/missing/read", body, cookie, csrf); w.Code != http.StatusNotFound {
		t.Fatalf("missing alert: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "POST", path, body, cookie, csrf); w.Code != http.StatusOK {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	readCount(0)
	current, err := s.Store.Alert(a.ID)
	if err != nil || current.Acknowledged || current.Revision != a.Revision {
		t.Fatalf("read changed alert workflow: %+v %v", current, err)
	}
}
