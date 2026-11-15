package model

import "time"

// OperationLog contains safe operational metadata, never a request/response body.
type OperationLog struct {
	ID           string         `json:"id"`
	At           time.Time      `json:"at"`
	LastAt       time.Time      `json:"lastAt"`
	Category     string         `json:"category"`
	Action       string         `json:"action"`
	ActorType    string         `json:"actorType"`
	ActorID      string         `json:"actorId"`
	ActorName    string         `json:"actorName"`
	AuthType     string         `json:"authType"`
	ResourceType string         `json:"resourceType"`
	ResourceID   string         `json:"resourceId"`
	Method       string         `json:"method"`
	Route        string         `json:"route"`
	IP           string         `json:"ip"`
	Status       int            `json:"status"`
	Result       string         `json:"result"`
	DurationMS   int64          `json:"durationMs"`
	RequestID    string         `json:"requestId"`
	JobID        string         `json:"jobId"`
	Details      map[string]any `json:"details"`
	Count        int64          `json:"count"`
}

type OperationLogFilter struct {
	Search, Actor, Action, IP, Method, AuthType, Result string
	From, To                                            time.Time
	Page, PageSize                                      int
}

type OperationLogPage struct {
	Items    []OperationLog `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
}
