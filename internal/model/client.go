package model

import "time"

type Assignment struct {
	NodeIDs       []string `json:"nodeIds"`
	DefaultNodeID string   `json:"defaultNodeId"`
	Revision      int64    `json:"revision"`
}
type ClientAccount struct {
	Username    string `json:"username"`
	Initialized bool   `json:"initialized"`
}
type ClientSession struct {
	ID                      string     `json:"id"`
	ClientName              string     `json:"clientName"`
	InstallationID          string     `json:"installationId"`
	CoreVersion             string     `json:"coreVersion"`
	DesiredNodeID           string     `json:"desiredNodeId"`
	ActualNodeID            string     `json:"actualNodeId"`
	RunState                string     `json:"runState"`
	Mode                    string     `json:"mode"`
	TUNEnabled              bool       `json:"tunEnabled"`
	ConnectionIntent        bool       `json:"connectionIntent"`
	DesiredConfigRevision   int64      `json:"desiredConfigRevision"`
	AppliedConfigRevision   int64      `json:"appliedConfigRevision"`
	ProcessedConfigRevision int64      `json:"processedConfigRevision"`
	LastSeen                *time.Time `json:"lastSeen"`
	LastError               string     `json:"lastError"`
	RevocationPending       bool       `json:"revocationPending"`
}
type ClientStatus struct {
	AppliedConfigRevision   int64  `json:"appliedConfigRevision"`
	ProcessedConfigRevision int64  `json:"processedConfigRevision"`
	DesiredNodeID           string `json:"desiredNodeId"`
	ActualNodeID            string `json:"actualNodeId"`
	RunState                string `json:"runState"`
	Mode                    string `json:"mode"`
	TUNEnabled              bool   `json:"tunEnabled"`
	ConnectionIntent        bool   `json:"connectionIntent"`
	CoreVersion             string `json:"coreVersion"`
	LastError               string `json:"lastError"`
}
type ClientNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Server   string `json:"server"`
	PublicIP string `json:"publicIp"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	SNI      string `json:"sni"`
	Ready    bool   `json:"ready"`
}
type RuleReference struct {
	Version int64  `json:"version"`
	Hash    string `json:"hash"`
	URL     string `json:"url"`
}
type ClientProfile struct {
	DeviceID        string        `json:"deviceId"`
	Name            string        `json:"name"`
	Enabled         bool          `json:"enabled"`
	Revision        int64         `json:"revision"`
	DefaultNodeID   string        `json:"defaultNodeId"`
	Nodes           []ClientNode  `json:"nodes"`
	Rules           RuleReference `json:"rules"`
	SubscriptionURL string        `json:"subscriptionUrl"`
}
type ClientTokens struct {
	AccessToken      string    `json:"accessToken"`
	RefreshToken     string    `json:"refreshToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	SessionID        string    `json:"sessionId"`
	Device           struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"device"`
}
type Removal struct {
	ID        string     `json:"id"`
	NodeID    string     `json:"nodeId"`
	DeviceID  string     `json:"deviceId"`
	Revision  int64      `json:"revision"`
	Status    string     `json:"status"`
	Deadline  *time.Time `json:"deadline"`
	CreatedAt time.Time  `json:"createdAt"`
	Immediate bool       `json:"immediate"`
}
