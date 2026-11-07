package model

import "time"

// PaymentSettings never includes a plaintext secret. Old encrypted config
// versions are retained solely to settle orders created using those versions.
type PaymentSettings struct {
	Enabled            bool   `json:"enabled"`
	APIBaseURL         string `json:"apiBaseUrl"`
	MerchantNum        string `json:"merchantNum"`
	PayType            string `json:"payType"`
	PayDurationMinutes int    `json:"payDurationMinutes"`
	QueryEnabled       bool   `json:"queryEnabled"`
	SecretConfigured   bool   `json:"secretConfigured"`
	Revision           int64  `json:"revision"`
	NotifyURL          string `json:"notifyUrl"`
}

type PaymentSettingsInput struct {
	Enabled            bool    `json:"enabled"`
	APIBaseURL         string  `json:"apiBaseUrl"`
	MerchantNum        string  `json:"merchantNum"`
	PayType            string  `json:"payType"`
	PayDurationMinutes int     `json:"payDurationMinutes"`
	QueryEnabled       bool    `json:"queryEnabled"`
	Secret             *string `json:"secret,omitempty"`
	ExpectedRevision   int64   `json:"expectedRevision"`
}

type PaymentOrderInput struct {
	RequestID        string `json:"requestId"`
	TemplateID       string `json:"templateId"`
	TemplateRevision int64  `json:"templateRevision"`
}

type PaymentOrder struct {
	ID               string               `json:"id"`
	OrderNo          string               `json:"orderNo"`
	UserID           string               `json:"userId"`
	Email            string               `json:"email,omitempty"`
	TemplateID       string               `json:"templateId"`
	TemplateName     string               `json:"templateName"`
	TemplateSnapshot SubscriptionTemplate `json:"templateSnapshot"`
	AmountCents      int64                `json:"amountCents"`
	Currency         string               `json:"currency"`
	ValidityDays     int                  `json:"validityDays"`
	CreatedAt        time.Time            `json:"createdAt"`
	ExpiresAt        time.Time            `json:"expiresAt"`
	PaidAt           *time.Time           `json:"paidAt,omitempty"`
	FulfilledAt      *time.Time           `json:"fulfilledAt,omitempty"`
	LastNotifyAt     *time.Time           `json:"lastNotifyAt,omitempty"`
	SubscriptionID   string               `json:"subscriptionId"`
	PayURL           string               `json:"payUrl,omitempty"`
	PlatformOrderNo  string               `json:"platformOrderNo,omitempty"`
	Status           string               `json:"status"`
	CreationState    string               `json:"creationState"`
	PaymentState     string               `json:"paymentState"`
	FulfillmentState string               `json:"fulfillmentState"`
	LastError        string               `json:"lastError"`
	Revision         int64                `json:"revision"`
	Activities       []PaymentActivity    `json:"activities,omitempty"`
}

type PaymentActivity struct {
	ID                string    `json:"id"`
	At                time.Time `json:"at"`
	Kind              string    `json:"kind"`
	Message           string    `json:"message"`
	ActualAmountCents *int64    `json:"actualAmountCents,omitempty"`
}

type PaymentOrderFilter struct {
	Status   string
	Search   string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}

type PaymentOrderList struct {
	Items    []PaymentOrder `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
}
