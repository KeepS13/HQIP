package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func TestAnnouncementAPIAuthenticationCSRFAndAudienceIsolation(t *testing.T) {
	s, h := newServer(t)
	admin, csrf := loginAdmin(t, s, h)
	u, cookie, userCSRF := portalLoginFixture(t, s, "announcement-api@example.com")
	_, otherCookie, otherCSRF := portalLoginFixture(t, s, "announcement-other@example.com")
	in := model.AnnouncementInput{ID: store.NewID(), Title: "公告", Body: "通知正文", Mode: "until_acknowledged", UserIDs: []string{u.ID}}
	for _, tc := range []struct {
		cookie, csrf string
		code         int
	}{{"", "", 401}, {cookie, userCSRF, 401}, {admin, "", 403}} {
		if w := request(h, "POST", "/api/v1/announcements", in, tc.cookie, tc.csrf); w.Code != tc.code {
			t.Fatal("admin auth", w.Code, w.Body.String())
		}
	}
	if w := request(h, "POST", "/api/v1/announcements", in, admin, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	public := s.PublicHandler()
	if w := portalRequest(public, "GET", "/api/v1/announcements", nil, cookie, userCSRF, s.opts.UserOrigin); w.Code != 404 {
		t.Fatal("admin route exposed publicly", w.Code)
	}
	if w := portalRequest(public, "GET", "/api/v1/user/announcements", nil, "", "", s.opts.UserOrigin); w.Code != 401 {
		t.Fatal("unauthenticated read", w.Code)
	}
	w := portalRequest(public, "GET", "/api/v1/user/announcements", nil, cookie, userCSRF, s.opts.UserOrigin)
	var page model.AnnouncementPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), "userIds") || strings.Contains(w.Body.String(), "recipientCount") {
		t.Fatal("private audience exposed", w.Body.String())
	}
	path := "/api/v1/user/announcements/" + in.ID + "/acknowledge"
	for _, tc := range []struct {
		cookie, csrf, origin string
		code                 int
	}{
		{cookie, "", s.opts.UserOrigin, 403}, {cookie, userCSRF, "https://evil.example", 403}, {otherCookie, otherCSRF, s.opts.UserOrigin, 404},
		{cookie, userCSRF, s.opts.UserOrigin, 200}, {cookie, userCSRF, s.opts.UserOrigin, 200},
	} {
		w = portalRequest(public, "POST", path, map[string]string{"userId": u.ID}, tc.cookie, tc.csrf, tc.origin)
		if w.Code != tc.code {
			t.Fatal("ack auth", w.Code, tc.code, w.Body.String())
		}
	}
	w = portalRequest(public, "GET", "/api/v1/user/announcements", nil, cookie, userCSRF, s.opts.UserOrigin)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Items) != 0 {
		t.Fatal("receipt not applied", w.Body.String())
	}
	w = request(h, "GET", "/api/v1/announcements", nil, admin, csrf)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Items[0].AcknowledgedCount != 1 {
		t.Fatal("admin receipts", w.Body.String())
	}
	if w = request(h, "DELETE", "/api/v1/announcements/"+in.ID, nil, admin, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
