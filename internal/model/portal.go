package model

import "time"

type Plan struct {
	SaleEnabled    bool     `json:"saleEnabled"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	NodeIDs        []string `json:"nodeIds"`
	DefaultNodeID  string   `json:"defaultNodeId"`
	BandwidthMbps  *float64 `json:"bandwidthMbps"`
	QuotaBytes     *int64   `json:"quotaBytes"`
	MaxConnections int      `json:"maxConnections"`
	Revision       int64    `json:"revision"`
}
type PortalUser struct {
	Revision    int64        `json:"revision"`
	ID          string       `json:"id"`
	Email       string       `json:"email"`
	Enabled     bool         `json:"enabled"`
	CreatedAt   time.Time    `json:"createdAt"`
	Entitlement *Entitlement `json:"entitlement,omitempty"`
}
type Entitlement struct {
	ID             string     `json:"id"`
	PlanID         string     `json:"planId"`
	PlanName       string     `json:"planName"`
	Status         string     `json:"status"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	QuotaBytes     *int64     `json:"quotaBytes"`
	UsedBytes      int64      `json:"usedBytes"`
	ReservedBytes  int64      `json:"reservedBytes"`
	BandwidthMbps  *float64   `json:"bandwidthMbps"`
	MaxConnections int        `json:"maxConnections"`
	Occupied       int        `json:"occupied"`
	NodeIDs        []string   `json:"nodeIds"`
	DefaultNodeID  string     `json:"defaultNodeId"`
	Epoch          int64      `json:"epoch"`
	Revision       int64      `json:"revision"`
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
	RateVersion             int64     `json:"rateVersion"`
	RatePending             bool      `json:"ratePending"`
	SessionID               string    `json:"sessionId,omitempty"`
	State                   string    `json:"state"`
	LastError               string    `json:"lastError"`
	ID                      string    `json:"id"`
	LeaseID                 string    `json:"leaseId"`
	UserID                  string    `json:"userId"`
	AccountingID            string    `json:"accountingId"`
	NodeID                  string    `json:"nodeId"`
	CreatedAt               time.Time `json:"createdAt"`
	ExpiresAt               time.Time `json:"expiresAt"`
	BandwidthBytesPerSecond int64     `json:"bandwidthBytesPerSecond"`
	BudgetBytes             int64     `json:"budgetBytes"`
	ConsumedBytes           int64     `json:"consumedBytes"`
	UploadBytes             int64     `json:"uploadBytes"`
	DownloadBytes           int64     `json:"downloadBytes"`
	Revision                int64     `json:"revision"`
	Closed                  bool      `json:"closed"`
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
