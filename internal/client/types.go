// Package client implements the installation-scoped desktop controller independently
// of its Windows user interface. Secrets never appear in View.
package client

import "time"

const CoreVersion = "1.14.4"

type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Server   string `json:"server"`
	PublicIP string `json:"publicIp"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	SNI      string `json:"sni"`
	Ready    bool   `json:"ready"`
}
type RulesRef struct {
	Version int64  `json:"version"`
	Hash    string `json:"hash"`
	URL     string `json:"url"`
}
type Profile struct {
	DeviceID          string         `json:"deviceId"`
	Name              string         `json:"name"`
	Enabled           bool           `json:"enabled"`
	Revision          int64          `json:"revision"`
	DefaultNodeID     string         `json:"defaultNodeId"`
	Nodes             []Node         `json:"nodes"`
	Rules             RulesRef       `json:"rules"`
	SubscriptionID    string         `json:"subscriptionId"`
	Subscription      *Subscription  `json:"subscription"`
	Subscriptions     []Subscription `json:"subscriptions"`
	SelectionRevision int64          `json:"selectionRevision"`
}

type Subscription struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId"`
	TemplateID     string    `json:"templateId"`
	TemplateName   string    `json:"templateName"`
	CreatedAt      time.Time `json:"createdAt"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expiresAt"`
	QuotaBytes     *int64    `json:"quotaBytes"`
	UsedBytes      int64     `json:"usedBytes"`
	ReservedBytes  int64     `json:"reservedBytes"`
	BandwidthMbps  *float64  `json:"bandwidthMbps"`
	MaxConnections int       `json:"maxConnections"`
	Occupied       int       `json:"occupied"`
	NodeIDs        []string  `json:"nodeIds"`
	DefaultNodeID  string    `json:"defaultNodeId"`
	Revision       int64     `json:"revision"`
}
type Tokens struct {
	AccessToken      string    `json:"accessToken"`
	RefreshToken     string    `json:"refreshToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	SessionID        string    `json:"sessionId"`
	User             struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}
type Status struct {
	DesiredSubscriptionID   string `json:"desiredSubscriptionId"`
	ActualSubscriptionID    string `json:"actualSubscriptionId"`
	ProcessedConfigRevision int64  `json:"processedConfigRevision"`
	AppliedConfigRevision   int64  `json:"appliedConfigRevision"`
	DesiredNodeID           string `json:"desiredNodeId"`
	ActualNodeID            string `json:"actualNodeId"`
	RunState                string `json:"runState"`
	Mode                    string `json:"mode"`
	TUNEnabled              bool   `json:"tunEnabled"`
	ConnectionIntent        bool   `json:"connectionIntent"`
	CoreVersion             string `json:"coreVersion"`
	LastError               string `json:"lastError"`
}
type NodeView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}
type View struct {
	LoggedIn          bool           `json:"loggedIn"`
	ServerURL         string         `json:"serverUrl"`
	DeviceName        string         `json:"deviceName"`
	Nodes             []NodeView     `json:"nodes"`
	DefaultNodeID     string         `json:"defaultNodeId"`
	Revision          int64          `json:"revision"`
	RulesVersion      int64          `json:"rulesVersion"`
	LastSync          *time.Time     `json:"lastSync"`
	SyncError         string         `json:"syncError"`
	Subscription      *Subscription  `json:"subscription"`
	Subscriptions     []Subscription `json:"subscriptions"`
	SelectionRevision int64          `json:"selectionRevision"`
	SelectionPending  bool           `json:"selectionPending"`
	Metrics           Dashboard      `json:"metrics"`
	LoginPending      bool           `json:"loginPending"`
	Connections       Connections    `json:"connections"`
	Status
}
type persisted struct {
	AccountVersion         int                            `json:"accountVersion"`
	ServerURL              string                         `json:"serverUrl"`
	InstallationID         string                         `json:"installationId"`
	Tokens                 Tokens                         `json:"tokens"`
	Profile                Profile                        `json:"profile"`
	SelectedNodeID         string                         `json:"selectedNodeId"`
	SelectedSubscriptionID string                         `json:"selectedSubscriptionId"`
	Preferences            map[string]SelectionPreference `json:"preferences"`
	Mode                   string                         `json:"mode"`
	TUN                    bool                           `json:"tun"`
}

type SelectionPreference struct {
	SubscriptionID string            `json:"subscriptionId"`
	NodeIDs        map[string]string `json:"nodeIds"`
}
