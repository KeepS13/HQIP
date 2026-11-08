package model

import "time"

// NodeAudience counts people separately from subscriptions and connections.
// A nil online value means the node cannot currently provide a fresh sample.
type NodeAudience struct {
	SubscribedUsers     int        `json:"subscribedUsers"`
	ActiveSubscriptions int        `json:"activeSubscriptions"`
	OnlineUsers         *int       `json:"onlineUsers"`
	OnlineConnections   *int       `json:"onlineConnections"`
	OnlineReportedAt    *time.Time `json:"onlineReportedAt"`
}

type NodeListItem struct {
	Node
	Audience NodeAudience `json:"audience"`
}
