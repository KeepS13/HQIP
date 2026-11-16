package model

import "time"

type TrafficDay struct {
	Date     string `json:"date"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Total    int64  `json:"total"`
	HasData  bool   `json:"hasData"`
}

type TrafficSummary struct {
	Upload       int64   `json:"upload"`
	Download     int64   `json:"download"`
	Total        int64   `json:"total"`
	DailyAverage float64 `json:"dailyAverage"`
	RecordedDays int     `json:"recordedDays"`
	PeakDate     string  `json:"peakDate,omitempty"`
	HasData      bool    `json:"hasData"`
}

type TrafficContribution struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Deleted  bool   `json:"deleted"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Total    int64  `json:"total"`
}

type TrafficAnalytics struct {
	From            string                `json:"from"`
	To              string                `json:"to"`
	MinDate         string                `json:"minDate"`
	Today           string                `json:"today"`
	TimeZone        string                `json:"timeZone"`
	IncludesToday   bool                  `json:"includesToday"`
	DayCount        int                   `json:"dayCount"`
	Summary         TrafficSummary        `json:"summary"`
	Days            []TrafficDay          `json:"days"`
	ContributorKind string                `json:"contributorKind"`
	Contributors    []TrafficContribution `json:"contributors"`
	GeneratedAt     time.Time             `json:"generatedAt"`
}

// RuntimeOverview describes observed proxy connections, not login sessions or
// reserved leases. Rates retain the Agent's existing bytes/second semantics.
type RuntimeOverview struct {
	OnlineStatus         string     `json:"onlineStatus"`
	RatesStatus          string     `json:"ratesStatus"`
	OnlineUsers          int        `json:"onlineUsers"`
	ActiveSubscriptions  int        `json:"activeSubscriptions"`
	Instances            int        `json:"instances"`
	UnmappedInstances    int        `json:"unmappedInstances"`
	ExpectedNodes        int        `json:"expectedNodes"`
	OnlineReportingNodes int        `json:"onlineReportingNodes"`
	ReportingNodes       int        `json:"reportingNodes"`
	UploadRate           float64    `json:"uploadRate"`
	DownloadRate         float64    `json:"downloadRate"`
	LastReportedAt       *time.Time `json:"lastReportedAt"`
}
