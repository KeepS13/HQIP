package control

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/store"
)

func TestPrepareAccountImportsAdminBoundary(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	_, userCookie, _ := portalLoginFixture(t, s, "prepare-links@example.com")
	path := "/api/v1/subscription-import-links/prepare"
	for _, auth := range []struct {
		cookie, csrf string
		status       int
	}{{"", "", 401}, {userCookie, "", 401}, {admin, "", 403}} {
		if w := request(h, "POST", path, map[string]any{}, auth.cookie, auth.csrf); w.Code != auth.status {
			t.Fatal("auth boundary", w.Code)
		}
	}
	w := request(h, "POST", path, map[string]any{}, admin, csrf)
	var result store.AccountImportPreparation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Created != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	var keys map[string]any
	if json.Unmarshal(w.Body.Bytes(), &keys) != nil || len(keys) != 3 {
		t.Fatal("response must contain counts only")
	}
}
