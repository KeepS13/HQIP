package model

import "time"

// ServerBilling is an advisory hosting cycle, independent of user subscriptions.
type ServerBilling struct {
	Status         string     `json:"status"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	CycleID        string     `json:"cycleId"`
	CycleStartedAt *time.Time `json:"cycleStartedAt"`
	NextResetAt    *time.Time `json:"nextResetAt"`
	CycleUpload    int64      `json:"cycleUpload"`
	CycleDownload  int64      `json:"cycleDownload"`
	CycleComplete  bool       `json:"cycleComplete"`
}

type ServerRenewal struct {
	ID                string     `json:"id"`
	ServerID          string     `json:"serverId"`
	RequestID         string     `json:"requestId"`
	Kind              string     `json:"kind"`
	Months            int        `json:"months"`
	PreviousExpiresAt *time.Time `json:"previousExpiresAt"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	ConfirmedAt       time.Time  `json:"confirmedAt"`
	Actor             string     `json:"actor"`
	Revision          int64      `json:"revision"`
}
