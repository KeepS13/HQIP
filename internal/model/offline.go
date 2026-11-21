package model

import (
	"github.com/apernet/hysteria/core/v2/billing"
	"time"
)

const OfflineAdmissionVersion = 1
const OfflineGrace = 24 * time.Hour
const EmergencyAllowanceBytes int64 = 1 << 30
const OfflineChunkBytes int64 = 16 << 20

// OfflineGrant caches verified subscription identity and durable metering.
// Authorization survives release of the online slot; a released slot must be
// acquired again before issuing a child lease. Version 3 counts HY2 instances
// separately and shares VLESS slots only within the same observed ClientIP.
type OfflineGrant struct {
	// ClientIP binds cached reconnects to the same observed source address.
	ClientIP                string             `json:"clientIP,omitempty"`
	ConnectionSlotsVersion  int                `json:"connectionSlotsVersion,omitempty"`
	SlotReleased            bool               `json:"slotReleased,omitempty"`
	NodeQuota               *NodeQuotaSnapshot `json:"nodeQuota,omitempty"`
	Billing                 *billing.Grant     `json:"billing,omitempty"`
	Version                 int                `json:"version"`
	ID                      string             `json:"id"`
	NodeID                  string             `json:"nodeId"`
	UserID                  string             `json:"userId"`
	SubscriptionID          string             `json:"subscriptionId"`
	SessionID               string             `json:"sessionId"`
	CredentialHash          string             `json:"credentialHash"`
	AuthorizedAt            time.Time          `json:"authorizedAt"`
	OfflineUntil            time.Time          `json:"offlineUntil"`
	NormalBudgetBytes       int64              `json:"normalBudgetBytes"`
	NormalUnlimited         bool               `json:"normalUnlimited"`
	EmergencyBudgetBytes    int64              `json:"emergencyBudgetBytes"`
	EmergencyUsedBytes      int64              `json:"emergencyUsedBytes"`
	UploadBytes             int64              `json:"uploadBytes"`
	DownloadBytes           int64              `json:"downloadBytes"`
	Revision                int64              `json:"revision"`
	RateVersion             int64              `json:"rateVersion"`
	BandwidthBytesPerSecond int64              `json:"bandwidthBytesPerSecond"`
	RatePending             bool               `json:"ratePending"`
	Closed                  bool               `json:"closed"`
	Revoked                 bool               `json:"revoked"`
	ReportVersion           int64              `json:"reportVersion"`
}

const NodeQuotaCapability = "node-quota-v1"

// NodeQuotaSnapshot is a transactionally consistent global balance and this
// node's acknowledgement watermark. Units are 1/100 billed byte. Revision is
// monotonic within a subscription, including across its different nodes.
type NodeQuotaSnapshot struct {
	Revision            int64  `json:"revision"`
	QuotaUnits          *int64 `json:"quotaUnits"`
	UsedUnits           int64  `json:"usedUnits"`
	NodeUsedUnits       int64  `json:"nodeUsedUnits"`
	LegacyReservedUnits int64  `json:"legacyReservedUnits"`
}

// Totals cover every child of a grant, including closed children. A replay of
// the same version must have exactly the same content.
type OfflineUsage struct {
	// SlotAction is an explicit, replayable transition (release/acquire).
	// Release proves all children ended; it never declares unknown usage final.
	SlotAction string `json:"slotAction,omitempty"`
	// RetainedNormalUnits retires a legacy parent and returns only never-issued
	// budget. All closed and uncertain children remain counted, in 1/100 bytes.
	RetainedNormalUnits *int64            `json:"retainedNormalUnits,omitempty"`
	Billing             []billing.Segment `json:"billing,omitempty"`
	Version             int64             `json:"version"`
	UploadBytes         int64             `json:"uploadBytes"`
	DownloadBytes       int64             `json:"downloadBytes"`
	EmergencyUsedBytes  int64             `json:"emergencyUsedBytes"`
	AppliedRateVersion  int64             `json:"appliedRateVersion"`
	Active              bool              `json:"active"`
	Closed              bool              `json:"closed"`
}
