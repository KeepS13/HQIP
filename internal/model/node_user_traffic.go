package model

import "time"

// NodeUserTraffic merges every mapped subscription of one account on one node.
// Counters are lifetime raw proxy bytes, without quota billing multipliers.
// HasData distinguishes an absent lifetime record from a recorded zero.
type NodeUserTraffic struct {
	UserID   string `json:"userId"`
	Email    string `json:"email"`
	Enabled  bool   `json:"enabled"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Total    int64  `json:"total"`
	HasData  bool   `json:"hasData"`
}

// NodeUserTrafficPage is an administrator-only snapshot, ordered by Total
// descending and UserID ascending for stable ties. Items is always an array.
type NodeUserTrafficPage struct {
	NodeID      string            `json:"nodeId"`
	Items       []NodeUserTraffic `json:"items"`
	TotalUsers  int               `json:"totalUsers"`
	Offset      int               `json:"offset"`
	Limit       int               `json:"limit"`
	GeneratedAt time.Time         `json:"generatedAt"`
}
