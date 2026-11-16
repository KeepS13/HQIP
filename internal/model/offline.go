package model

import "time"

const OfflineAdmissionVersion = 1
const OfflineGrace = 24 * time.Hour
const EmergencyAllowanceBytes int64 = 1 << 30
const OfflineChunkBytes int64 = 16 << 20

// OfflineGrant owns one centrally reserved slot. Child QUIC leases never own
// another slot and may only spend this grant's durably delegated budgets.
type OfflineGrant struct {
	Version                 int       `json:"version"`
	ID                      string    `json:"id"`
	NodeID                  string    `json:"nodeId"`
	UserID                  string    `json:"userId"`
	SubscriptionID          string    `json:"subscriptionId"`
	SessionID               string    `json:"sessionId"`
	CredentialHash          string    `json:"credentialHash"`
	AuthorizedAt            time.Time `json:"authorizedAt"`
	OfflineUntil            time.Time `json:"offlineUntil"`
	NormalBudgetBytes       int64     `json:"normalBudgetBytes"`
	NormalUnlimited         bool      `json:"normalUnlimited"`
	EmergencyBudgetBytes    int64     `json:"emergencyBudgetBytes"`
	EmergencyUsedBytes      int64     `json:"emergencyUsedBytes"`
	UploadBytes             int64     `json:"uploadBytes"`
	DownloadBytes           int64     `json:"downloadBytes"`
	Revision                int64     `json:"revision"`
	RateVersion             int64     `json:"rateVersion"`
	BandwidthBytesPerSecond int64     `json:"bandwidthBytesPerSecond"`
	RatePending             bool      `json:"ratePending"`
	Closed                  bool      `json:"closed"`
	Revoked                 bool      `json:"revoked"`
	ReportVersion           int64     `json:"reportVersion"`
}

// Totals cover every child of a grant, including closed children. A replay of
// the same version must have exactly the same content.
type OfflineUsage struct {
	Version            int64 `json:"version"`
	UploadBytes        int64 `json:"uploadBytes"`
	DownloadBytes      int64 `json:"downloadBytes"`
	EmergencyUsedBytes int64 `json:"emergencyUsedBytes"`
	AppliedRateVersion int64 `json:"appliedRateVersion"`
	Active             bool  `json:"active"`
	Closed             bool  `json:"closed"`
}
