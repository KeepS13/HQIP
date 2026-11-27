package store

import (
	"database/sql"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrAnnouncementInput = errors.New("公告输入无效：请检查标题、正文、展示方式与接收用户")
var announcementID = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`)

func normalizeAnnouncement(in model.AnnouncementInput) (model.AnnouncementInput, error) {
	in.Title, in.Body = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	if !announcementID.MatchString(in.ID) || in.Title == "" || utf8.RuneCountInString(in.Title) > 120 ||
		in.Body == "" || utf8.RuneCountInString(in.Body) > 10000 || strings.ContainsRune(in.Title+in.Body, 0) ||
		(in.Mode != "always" && in.Mode != "until_acknowledged") || len(in.UserIDs) == 0 || len(in.UserIDs) > 5000 {
		return in, ErrAnnouncementInput
	}
	// Copy before sorting: callers retain ownership of the request slice.
	in.UserIDs = slices.Clone(in.UserIDs)
	slices.Sort(in.UserIDs)
	in.UserIDs = slices.Compact(in.UserIDs)
	for _, id := range in.UserIDs {
		if id == "" || len(id) > 128 {
			return in, ErrAnnouncementInput
		}
	}
	return in, nil
}

// PublishAnnouncement atomically publishes an immutable message and explicit
// audience. The caller-provided ID is the idempotency key; reusing it for other
// content returns ErrConflict, including after the original was stopped.
func (s *Store) PublishAnnouncement(input model.AnnouncementInput) (model.Announcement, error) {
	in, err := normalizeAnnouncement(input)
	if err != nil {
		return model.Announcement{}, err
	}
	err = retryDatabaseError(func() error {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.Exec(`INSERT INTO announcements(id,title,body,mode,created_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, in.ID, in.Title, in.Body, in.Mode, s.Now().Unix())
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 0 {
			if err = compareAnnouncement(tx, in); err != nil {
				return err
			}
		} else if err = insertAnnouncementRecipients(tx, in); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return model.Announcement{}, err
	}
	return s.AnnouncementDetail(in.ID)
}

func compareAnnouncement(tx *sql.Tx, in model.AnnouncementInput) error {
	var title, body, mode string
	if err := tx.QueryRow("SELECT title,body,mode FROM announcements WHERE id=?", in.ID).Scan(&title, &body, &mode); err != nil {
		return err
	}
	if title != in.Title || body != in.Body || mode != in.Mode {
		return ErrConflict
	}
	ids, err := announcementRecipients(tx, in.ID)
	if err != nil {
		return err
	}
	if !slices.Equal(ids, in.UserIDs) {
		return ErrConflict
	}
	return nil
}

func insertAnnouncementRecipients(tx *sql.Tx, in model.AnnouncementInput) error {
	// Bound SQL parameters for SQLite as well as PostgreSQL. Validate the whole
	// audience by affected-row counts; a missing user rolls back earlier chunks.
	for start := 0; start < len(in.UserIDs); start += 200 {
		ids := in.UserIDs[start:min(start+200, len(in.UserIDs))]
		args := []any{in.ID}
		for _, id := range ids {
			args = append(args, id)
		}
		result, err := tx.Exec(`INSERT INTO announcement_recipients(announcement_id,user_id) SELECT ?,id FROM portal_users WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != int64(len(ids)) {
			return ErrAnnouncementInput
		}
	}
	return nil
}

// StopAnnouncement is idempotent. Records remain available for stable cursors
// and publish retries, but are immediately excluded from subsequent user reads.
func (s *Store) StopAnnouncement(id string) error {
	result, err := s.DB.Exec("UPDATE announcements SET active=0 WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// AcknowledgeAnnouncement uses only the authenticated user. A single conditional
// update makes retries idempotent and preserves the first acknowledgement time.
func (s *Store) AcknowledgeAnnouncement(user, id string) error {
	return retryDatabaseError(func() error {
		result, err := s.DB.Exec(`UPDATE announcement_recipients SET acknowledged_at=CASE WHEN acknowledged_at=0 THEN ? ELSE acknowledged_at END
		WHERE announcement_id=? AND user_id=? AND EXISTS(SELECT 1 FROM announcements WHERE id=? AND active=1)`, s.Now().Unix(), id, user, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return ErrNotFound
		}
		return err
	})
}
