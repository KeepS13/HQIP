package model

type AlertUnreadSummary struct {
	Unread int `json:"unread"`
}

type AlertReadUpdate struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}
