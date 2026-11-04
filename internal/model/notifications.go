package model

import "time"

// NotificationCondition is a safe, rendered observation, never a raw log. Key
// identifies one resource/condition/period and deliberately excludes Stage.
type NotificationCondition struct {
	Key             string `json:"key"`
	Category        string `json:"category"`
	Kind            string `json:"kind"`
	ResourceID      string `json:"resourceId"`
	Title           string `json:"title"`
	Message         string `json:"message"`
	DetailPath      string `json:"detailPath"`
	Level           string `json:"level"`
	Stage           int    `json:"stage"`
	Continuous      bool   `json:"continuous"`
	DelaySeconds    int    `json:"delaySeconds"`
	RecoverySeconds int    `json:"recoverySeconds"`
	Unknown         bool   `json:"unknown,omitempty"`
	Removed         bool   `json:"removed,omitempty"`
}

type NotificationCategories struct {
	System      bool `json:"system"`
	Capacity    bool `json:"capacity"`
	Certificate bool `json:"certificate"`
	Package     bool `json:"package"`
	Recovery    bool `json:"recovery"`
}

func DefaultNotificationCategories() NotificationCategories {
	return NotificationCategories{System: true, Capacity: true, Certificate: true, Package: true, Recovery: true}
}

func (c NotificationCategories) Allows(category string) bool {
	switch category {
	case "system":
		return c.System
	case "capacity":
		return c.Capacity
	case "certificate":
		return c.Certificate
	case "package":
		return c.Package
	case "recovery", "recovery-summary":
		return c.Recovery
	case "summary", "test":
		return true
	default:
		return false
	}
}

type FeishuSettings struct {
	Enabled           bool                   `json:"enabled"`
	WebhookConfigured bool                   `json:"webhookConfigured"`
	WebhookMasked     string                 `json:"webhookMasked"`
	SecretConfigured  bool                   `json:"secretConfigured"`
	Categories        NotificationCategories `json:"categories"`
	Revision          int64                  `json:"revision"`
	LastSuccessAt     *time.Time             `json:"lastSuccessAt"`
	LastError         string                 `json:"lastError"`
	QueueDepth        int                    `json:"queueDepth"`
}

type FeishuSettingsInput struct {
	Enabled          bool                   `json:"enabled"`
	Webhook          string                 `json:"webhook"`
	Secret           string                 `json:"secret"`
	ClearCredentials bool                   `json:"clearCredentials"`
	Categories       NotificationCategories `json:"categories"`
	ExpectedRevision int64                  `json:"expectedRevision"`
}

type NotificationRecord struct {
	ID          string     `json:"id"`
	Category    string     `json:"category"`
	Level       string     `json:"level"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	DetailPath  string     `json:"detailPath"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	CreatedAt   time.Time  `json:"createdAt"`
	NextAttempt *time.Time `json:"nextAttempt"`
	LastError   string     `json:"lastError"`
	CanRetry    bool       `json:"canRetry"`
}

type NotificationPage struct {
	Items    []NotificationRecord `json:"items"`
	Total    int                  `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

// NotificationBatch carries credentials only in memory to the sender. They
// must never be serialized, logged or included in delivery history.
type NotificationBatch struct {
	Claim    string               `json:"-"`
	Revision int64                `json:"-"`
	Webhook  string               `json:"-"`
	Secret   string               `json:"-"`
	Records  []NotificationRecord `json:"records"`
}
