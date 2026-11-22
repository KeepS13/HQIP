package control

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestSubscriptionAssignmentRecoveryContract(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	u, _, _ := portalLoginFixture(t, s, "assignment-recovery@example.com")
	node := allocationNode(t, s)
	p, err := s.Store.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: "gift", Enabled: true, ValidityDays: 7, MaxConnections: 1, NodesPerSubscription: 1, NodeIDs: []string{node.ID}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/users/" + u.ID + "/subscriptions"
	body := map[string]any{"templateId": p.ID, "requestId": "recoverable-http-gift", "expectedRevision": u.Revision, "templateRevision": p.Revision}
	check := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var response struct{ Code string }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != status || response.Code != code {
			t.Fatal("unexpected assignment response", w.Code, w.Body.String(), err)
		}
	}
	// Authorization failures have no terminal assignment code: the original
	// request might have committed before the administrator session expired.
	check(request(h, "POST", path, body, "", ""), 401, "")
	check(request(h, "POST", path, body, admin, ""), 403, "")
	check(request(h, "POST", path, map[string]any{"requestId": "short"}, admin, csrf), 400, "SUBSCRIPTION_ASSIGNMENT_REJECTED")
	trigger := `CREATE TRIGGER assignment_failure BEFORE INSERT ON audit WHEN NEW.action='subscription.assigned' BEGIN SELECT RAISE(ABORT,'private database details'); END`
	if s.Store.Postgres() {
		if _, err = s.Store.DB.Exec(`CREATE FUNCTION assignment_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.assigned' THEN RAISE EXCEPTION 'private database details'; END IF; RETURN NEW; END $$`); err != nil {
			t.Fatal(err)
		}
		trigger = `CREATE TRIGGER assignment_failure BEFORE INSERT ON audit FOR EACH ROW EXECUTE FUNCTION assignment_failure()`
	}
	if _, err = s.Store.DB.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", path, body, admin, csrf)
	check(w, 503, "SUBSCRIPTION_ASSIGNMENT_UNAVAILABLE")
	if w.Header().Get("Retry-After") != "1" || strings.Contains(w.Body.String(), "private") {
		t.Fatal("database error leaked or omitted retry advice", w.Header(), w.Body.String())
	}
	drop := "DROP TRIGGER assignment_failure"
	if s.Store.Postgres() {
		drop += " ON audit"
	}
	if _, err = s.Store.DB.Exec(drop); err != nil {
		t.Fatal(err)
	}
	var first model.UserSubscription
	for i := 0; i < 2; i++ {
		w = request(h, "POST", path, body, admin, csrf)
		check(w, 200, "")
		var sub model.UserSubscription
		if err = json.Unmarshal(w.Body.Bytes(), &sub); err != nil || sub.ID == "" || (i > 0 && sub.ID != first.ID) {
			t.Fatal("replay duplicated the gift", w.Body.String(), err)
		}
		first = sub
	}
	body["requestId"] = "another-gift-request"
	check(request(h, "POST", path, body, admin, csrf), 409, "SUBSCRIPTION_ASSIGNMENT_REJECTED")
	body["expectedRevision"] = u.Revision + 1
	check(request(h, "POST", path, body, admin, csrf), 409, "SUBSCRIPTION_ASSIGNMENT_REJECTED") // stock
	if _, err = s.Store.DB.Exec("UPDATE portal_users SET enabled=0 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	check(request(h, "POST", path, body, admin, csrf), 403, "SUBSCRIPTION_ASSIGNMENT_REJECTED")
	var count int
	if err = s.Store.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='subscription.assigned' AND subject=?", first.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("HTTP recovery duplicated audit", count, err)
	}
}
