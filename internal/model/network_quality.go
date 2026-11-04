package model

import (
	"encoding/json"
	"time"
)

// NetworkQualityStatus separates the last attempt from the last valid report.
type NetworkQualityStatus struct {
	NodeID         string          `json:"nodeId"`
	Status         string          `json:"status"`
	JobID          int64           `json:"jobId"`
	SourceIP       string          `json:"sourceIp"`
	RequestedAt    *time.Time      `json:"requestedAt"`
	StartedAt      *time.Time      `json:"startedAt"`
	FinishedAt     *time.Time      `json:"finishedAt"`
	Error          string          `json:"error"`
	ReportAt       *time.Time      `json:"reportAt"`
	ReportSourceIP string          `json:"reportSourceIp"`
	Report         json.RawMessage `json:"report"`
}
