package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func announcementUsers(t *testing.T, s *Store) []string {
	t.Helper()
	var ids []string
	for i := 0; i < 3; i++ {
		u, err := s.RegisterPortal(fmt.Sprintf("announcement-%d@example.com", i), "password-for-test")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	return ids
}

func TestAnnouncementAudienceModesIdempotencyAndStop(t *testing.T) {
	s, now := fixture(t)
	users := announcementUsers(t, s)
	in := model.AnnouncementInput{ID: NewID(), Title: "维护通知", Body: "第一行\n第二行 <script>纯文本</script>", Mode: "until_acknowledged", UserIDs: []string{users[1], users[0], users[1]}}
	a, err := s.PublishAnnouncement(in)
	if err != nil || len(a.UserIDs) != 2 {
		t.Fatalf("publish: %+v %v", a, err)
	}
	if in.UserIDs[0] != users[1] {
		t.Fatal("request slice mutated")
	}
	replayed, err := s.PublishAnnouncement(in)
	if err != nil || replayed.ID != a.ID {
		t.Fatal("replay", err)
	}
	bad := in
	bad.Body = "changed"
	if _, err = s.PublishAnnouncement(bad); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	for _, user := range users[:2] {
		p, err := s.Announcements(user, "")
		if err != nil || len(p.Items) != 1 || len(p.Items[0].UserIDs) != 0 || p.Items[0].RecipientCount != 0 {
			t.Fatal("audience leak", p, err)
		}
	}
	outsider, err := s.Announcements(users[2], "")
	if err != nil || len(outsider.Items) != 0 {
		t.Fatal("wrong audience", outsider, err)
	}
	if err = s.AcknowledgeAnnouncement(users[2], a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("outsider ack", err)
	}
	if err = s.AcknowledgeAnnouncement(users[0], a.ID); err != nil {
		t.Fatal(err)
	}
	first := now.Unix()
	*now = now.Add(time.Hour)
	if err = s.AcknowledgeAnnouncement(users[0], a.ID); err != nil {
		t.Fatal(err)
	}
	var ack int64
	if err = s.DB.QueryRow("SELECT acknowledged_at FROM announcement_recipients WHERE announcement_id=? AND user_id=?", a.ID, users[0]).Scan(&ack); err != nil || ack != first {
		t.Fatal("first receipt changed", ack, err)
	}
	hidden, _ := s.Announcements(users[0], "")
	other, _ := s.Announcements(users[1], "")
	if len(hidden.Items) != 0 || len(other.Items) != 1 {
		t.Fatal("receipt not per user")
	}
	in.ID, in.Mode = NewID(), "always"
	always, err := s.PublishAnnouncement(in)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgeAnnouncement(users[0], always.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		p, err := s.Announcements(users[0], "")
		if err != nil || len(p.Items) != 1 || p.Items[0].ID != always.ID {
			t.Fatal("always hidden by receipt", p, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err = s.StopAnnouncement(always.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.AcknowledgeAnnouncement(users[0], always.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("stopped ack", err)
	}
	replayed, err = s.PublishAnnouncement(in)
	if err != nil || replayed.Active {
		t.Fatal("retry reactivated stopped announcement", err)
	}
}

func TestAnnouncementValidationAtomicAudienceAndPagination(t *testing.T) {
	s, _ := fixture(t)
	users := announcementUsers(t, s)
	valid := model.AnnouncementInput{ID: NewID(), Title: "标题", Body: "正文", Mode: "always", UserIDs: users[:2]}
	for _, change := range []func(*model.AnnouncementInput){
		func(a *model.AnnouncementInput) { a.ID = "short" },
		func(a *model.AnnouncementInput) { a.Title = strings.Repeat("字", 121) },
		func(a *model.AnnouncementInput) { a.Body = " " },
		func(a *model.AnnouncementInput) { a.Mode = "other" },
		func(a *model.AnnouncementInput) { a.UserIDs = nil },
		func(a *model.AnnouncementInput) { a.UserIDs = append([]string{users[0]}, "missing") },
	} {
		bad := valid
		change(&bad)
		if _, err := s.PublishAnnouncement(bad); !errors.Is(err, ErrAnnouncementInput) {
			t.Fatal("invalid input", err)
		}
	}
	page, err := s.Announcements("", "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal("failed publication left rows", page, err)
	}
	for i := 0; i < 24; i++ {
		in := valid
		in.ID = fmt.Sprintf("announcement-%04d", i)
		in.Mode = "until_acknowledged"
		if _, err = s.PublishAnnouncement(in); err != nil {
			t.Fatal(err)
		}
	}
	page, err = s.Announcements(users[0], "")
	if err != nil || len(page.Items) != 20 || page.NextCursor == "" {
		t.Fatal("first page", page, err)
	}
	for _, a := range page.Items {
		if err = s.AcknowledgeAnnouncement(users[0], a.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.StopAnnouncement(page.NextCursor); err != nil {
		t.Fatal(err)
	}
	next, err := s.Announcements(users[0], page.NextCursor)
	if err != nil || len(next.Items) != 4 || next.NextCursor != "" {
		t.Fatal("acknowledging shifted cursor", next, err)
	}
	if _, err = s.Announcements(users[2], page.NextCursor); !errors.Is(err, ErrNotFound) {
		t.Fatal("cursor leaked to outsider", err)
	}
}

func TestAnnouncementConcurrentPublishAndPersistentReceipt(t *testing.T) {
	s, _ := sqliteFixture(t)
	users := announcementUsers(t, s)
	in := model.AnnouncementInput{ID: NewID(), Title: "通知", Body: "确认一次", Mode: "until_acknowledged", UserIDs: users}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PublishAnnouncement(in); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	page, err := s.Announcements("", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].RecipientCount != 3 {
		t.Fatal("duplicate publication", page, err)
	}
	if err = s.AcknowledgeAnnouncement(users[0], in.ID); err != nil {
		t.Fatal(err)
	}
	if err = EnsureAnnouncementsSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.DataDir, s.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	page, err = reopened.Announcements(users[0], "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal("receipt lost on reopen", page, err)
	}
}
