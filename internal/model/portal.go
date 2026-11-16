package model

import "time"

type SubscriptionTemplate struct {
	Description          string                `json:"description"`
	NodesPerSubscription int                   `json:"nodesPerSubscription"`
	AllocationShortfalls []AllocationShortfall `json:"allocationShortfalls,omitempty"`
	PriceCents           *int64                `json:"priceCents"`
	Listed               bool                  `json:"listed"`
	ValidityDays         int                   `json:"validityDays"`
	ID                   string                `json:"id"`
	Name                 string                `json:"name"`
	Enabled              bool                  `json:"enabled"`
	NodeIDs              []string              `json:"nodeIds"`
	DefaultNodeID        string                `json:"defaultNodeId"`
	BandwidthMbps        *float64              `json:"bandwidthMbps"`
	QuotaBytes           *int64                `json:"quotaBytes"`
	MaxConnections       int                   `json:"maxConnections"`
	Revision             int64                 `json:"revision"`
}
type AllocationShortfall struct {
	SubscriptionID string `json:"subscriptionId,omitempty"`
	OrderID        string `json:"orderId,omitempty"`
	UserID         string `json:"userId"`
	Required       int    `json:"required"`
	Assigned       int    `json:"assigned"`
}
type CatalogSubscription struct {
	SubscriptionTemplate
	AvailableCopies    int    `json:"availableCopies"`
	AvailabilityReason string `json:"availabilityReason"`
}
type PortalUser struct {
	Revision      int64              `json:"revision"`
	ID            string             `json:"id"`
	Email         string             `json:"email"`
	Enabled       bool               `json:"enabled"`
	CreatedAt     time.Time          `json:"createdAt"`
	Subscriptions []UserSubscription `json:"subscriptions"`
}
type UserSubscription struct {
	EmergencyUsedBytes     int64     `json:"emergencyUsedBytes"`
	EmergencyReservedBytes int64     `json:"emergencyReservedBytes"`
	OfflineReservations    int       `json:"offlineReservations"`
	NodesPerSubscription   int       `json:"nodesPerSubscription"`
	ID                     string    `json:"id"`
	UserID                 string    `json:"userId"`
	TemplateID             string    `json:"templateId"`
	TemplateName           string    `json:"templateName"`
	CreatedAt              time.Time `json:"createdAt"`
	ExpiresAt              time.Time `json:"expiresAt"`
	Status                 string    `json:"status"`
	QuotaBytes             *int64    `json:"quotaBytes"`
	UsedBytes              int64     `json:"usedBytes"`
	ReservedBytes          int64     `json:"reservedBytes"`
	BandwidthMbps          *float64  `json:"bandwidthMbps"`
	MaxConnections         int       `json:"maxConnections"`
	Occupied               int       `json:"occupied"`
	NodeIDs                []string  `json:"nodeIds"`
	DefaultNodeID          string    `json:"defaultNodeId"`
	Revision               int64     `json:"revision"`
}
type SubscriptionRequest struct {
	ID             string     `json:"id"`
	UserID         string     `json:"userId"`
	Email          string     `json:"email"`
	TemplateID     string     `json:"templateId"`
	TemplateName   string     `json:"templateName"`
	Status         string     `json:"status"`
	RequestedAt    time.Time  `json:"requestedAt"`
	DecidedAt      *time.Time `json:"decidedAt,omitempty"`
	Note           string     `json:"note"`
	SubscriptionID string     `json:"subscriptionId"`
	Revision       int64      `json:"revision"`
}
type SubscriptionSelection struct {
	SubscriptionID    string `json:"subscriptionId"`
	SelectionRevision int64  `json:"selectionRevision"`
}
type PortalSession struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	ClientName string    `json:"clientName"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeen   time.Time `json:"lastSeen"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Revoked    bool      `json:"revoked"`
	Current    bool      `json:"current"`
}
type ConnectionLease struct {
	OfflineReservation      bool       `json:"offlineReservation,omitempty"`
	LastForwardingReport    *time.Time `json:"lastForwardingReport,omitempty"`
	RateVersion             int64      `json:"rateVersion"`
	RatePending             bool       `json:"ratePending"`
	SessionID               string     `json:"sessionId,omitempty"`
	State                   string     `json:"state"`
	LastError               string     `json:"lastError"`
	ID                      string     `json:"id"`
	LeaseID                 string     `json:"leaseId"`
	UserID                  string     `json:"userId"`
	SubscriptionID          string     `json:"subscriptionId"`
	AccountingID            string     `json:"accountingId"`
	NodeID                  string     `json:"nodeId"`
	CreatedAt               time.Time  `json:"createdAt"`
	ExpiresAt               time.Time  `json:"expiresAt"`
	BandwidthBytesPerSecond int64      `json:"bandwidthBytesPerSecond"`
	BudgetBytes             int64      `json:"budgetBytes"`
	ConsumedBytes           int64      `json:"consumedBytes"`
	UploadBytes             int64      `json:"uploadBytes"`
	DownloadBytes           int64      `json:"downloadBytes"`
	Revision                int64      `json:"revision"`
	Closed                  bool       `json:"closed"`
}
type LeaseUsage struct {
	AppliedRateVersion int64 `json:"appliedRateVersion"`
	UploadBytes        int64 `json:"uploadBytes"`
	DownloadBytes      int64 `json:"downloadBytes"`
	Closed             bool  `json:"closed"`
}
type PortalTokens struct {
	AccessToken      string     `json:"accessToken"`
	RefreshToken     string     `json:"refreshToken"`
	AccessExpiresAt  time.Time  `json:"accessExpiresAt"`
	RefreshExpiresAt time.Time  `json:"refreshExpiresAt"`
	SessionID        string     `json:"sessionId"`
	User             PortalUser `json:"user"`
}
