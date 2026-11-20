package model

import (
	"github.com/apernet/hysteria/core/v2/billing"
	"time"
)

type TrafficBillingSettings struct {
	Enabled       bool                   `json:"enabled"`
	Revision      int64                  `json:"revision"`
	Hysteria2     int64                  `json:"hysteria2"`
	VLESS         int64                  `json:"vless"`
	ActiveLeases  int64                  `json:"activeLeases"`
	PendingLeases int64                  `json:"pendingLeases"`
	History       []TrafficBillingChange `json:"history"`
}

type TrafficBillingInput struct {
	Enabled          bool  `json:"enabled"`
	ExpectedRevision int64 `json:"expectedRevision"`
	Hysteria2        int64 `json:"hysteria2"`
	VLESS            int64 `json:"vless"`
}

type TrafficBillingChange struct {
	Revision int64               `json:"revision"`
	Actor    string              `json:"actor"`
	At       time.Time           `json:"at"`
	Before   TrafficBillingInput `json:"before"`
	After    TrafficBillingInput `json:"after"`
}

type BillingDisplay struct {
	billing.Policy
	LeaseID        string    `json:"leaseId"`
	NodeID         string    `json:"nodeId"`
	SubscriptionID string    `json:"subscriptionId"`
	AppliedAt      time.Time `json:"appliedAt"`
	ValidUntil     time.Time `json:"validUntil"`
}
