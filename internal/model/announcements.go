package model

import "time"

// Announcement content is immutable after publication; stopping delivery does
// not erase receipts. Recipient IDs and aggregate counts are admin-only data.
type Announcement struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Body              string    `json:"body"`
	Mode              string    `json:"mode"`
	Active            bool      `json:"active"`
	CreatedAt         time.Time `json:"createdAt"`
	RecipientCount    int       `json:"recipientCount,omitempty"`
	AcknowledgedCount int       `json:"acknowledgedCount,omitempty"`
	UserIDs           []string  `json:"userIds,omitempty"`
}

type AnnouncementInput struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Mode    string   `json:"mode"`
	UserIDs []string `json:"userIds"`
}

type AnnouncementPage struct {
	Items      []Announcement `json:"items"`
	NextCursor string         `json:"nextCursor"`
}
