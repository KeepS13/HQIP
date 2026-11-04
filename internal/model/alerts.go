package model

import "time"

type Alert struct {
	ID             string                `json:"id"`
	Key            string                `json:"-"`
	Condition      NotificationCondition `json:"condition"`
	Status         string                `json:"status"`
	Acknowledged   bool                  `json:"acknowledged"`
	AcknowledgedBy string                `json:"acknowledgedBy"`
	AcknowledgedAt *time.Time            `json:"acknowledgedAt"`
	FirstSeen      time.Time             `json:"firstSeen"`
	LastSeen       time.Time             `json:"lastSeen"`
	EndedAt        *time.Time            `json:"endedAt"`
	Revision       int64                 `json:"revision"`
	Activities     []AlertActivity       `json:"activities,omitempty"`
}
type AlertActivity struct {
	ID      int64     `json:"id"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Actor   string    `json:"actor"`
	At      time.Time `json:"at"`
}
type AlertPage struct {
	Items    []Alert `json:"items"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
}
type AlertSummary struct {
	Active         int        `json:"active"`
	Unacknowledged int        `json:"unacknowledged"`
	Unknown        int        `json:"unknown"`
	Critical       int        `json:"critical"`
	LastEvaluation *time.Time `json:"lastEvaluation"`
}
type AlertUpdate struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	Acknowledge      bool   `json:"acknowledge"`
	Note             string `json:"note"`
}
