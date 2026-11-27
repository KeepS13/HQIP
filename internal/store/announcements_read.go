package store

import (
	"database/sql"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

const announcementColumns = "a.id,a.title,a.body,a.mode,a.active,a.created_at"

type announcementReader interface {
	Query(string, ...any) (*sql.Rows, error)
}

func announcementRecipients(db announcementReader, id string) ([]string, error) {
	rows, err := db.Query("SELECT user_id FROM announcement_recipients WHERE announcement_id=? ORDER BY user_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		ids = append(ids, value)
	}
	return ids, rows.Err()
}

func scanAnnouncement(row interface{ Scan(...any) error }, admin bool) (model.Announcement, error) {
	var a model.Announcement
	var created int64
	args := []any{&a.ID, &a.Title, &a.Body, &a.Mode, &a.Active, &created}
	if admin {
		args = append(args, &a.RecipientCount, &a.AcknowledgedCount)
	}
	err := row.Scan(args...)
	a.CreatedAt = time.Unix(created, 0).UTC()
	return a, err
}

// AnnouncementDetail is admin-only; user delivery never returns audience IDs.
func (s *Store) AnnouncementDetail(id string) (model.Announcement, error) {
	a, err := scanAnnouncement(s.DB.QueryRow("SELECT "+announcementColumns+" FROM announcements a WHERE a.id=?", id), false)
	if err != nil {
		return a, notFound(err)
	}
	a.UserIDs, err = announcementRecipients(s.DB, id)
	return a, err
}

// Announcements uses keyset pagination (20 rows) so acknowledging the current
// page cannot shift offsets and skip unread items. Empty user means admin view;
// handlers must obtain nonempty user IDs from the authenticated session.
func (s *Store) Announcements(user, before string) (model.AnnouncementPage, error) {
	page := model.AnnouncementPage{Items: []model.Announcement{}}
	admin := user == ""
	columns, from, where := announcementColumns, " FROM announcements a", " WHERE 1=1"
	args := []any{}
	if admin {
		columns += `,(SELECT COUNT(*) FROM announcement_recipients r WHERE r.announcement_id=a.id),
		(SELECT COUNT(*) FROM announcement_recipients r WHERE r.announcement_id=a.id AND r.acknowledged_at>0)`
	} else {
		from += " JOIN announcement_recipients r ON r.announcement_id=a.id"
		where += " AND r.user_id=? AND a.active=1 AND (a.mode='always' OR r.acknowledged_at=0)"
		args = append(args, user)
	}
	if before != "" {
		var created int64
		query := "SELECT created_at FROM announcements WHERE id=?"
		cursorArgs := []any{before}
		if !admin {
			query += " AND EXISTS(SELECT 1 FROM announcement_recipients WHERE announcement_id=? AND user_id=?)"
			cursorArgs = append(cursorArgs, before, user)
		}
		if err := s.DB.QueryRow(query, cursorArgs...).Scan(&created); err != nil {
			return page, notFound(err)
		}
		where += " AND (a.created_at<? OR (a.created_at=? AND a.id<?))"
		args = append(args, created, created, before)
	}
	rows, err := s.DB.Query("SELECT "+columns+from+where+" ORDER BY a.created_at DESC,a.id DESC LIMIT 21", args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAnnouncement(rows, admin)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, a)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > 20 {
		page.Items = page.Items[:20]
		page.NextCursor = page.Items[19].ID
	}
	return page, nil
}
