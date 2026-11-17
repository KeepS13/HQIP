package model

// ClientNodeEndpoint is the credential-free catalog used to choose and probe
// nodes across a user's valid subscriptions. Proxy credentials remain scoped
// to the explicitly selected subscription in ClientProfile.Nodes.
type ClientNodeEndpoint struct {
	Protocol string           `json:"protocol,omitempty"`
	NodeID   string           `json:"nodeId,omitempty"`
	Reality  *RealityEndpoint `json:"reality,omitempty"`
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Server   string           `json:"server"`
	PublicIP string           `json:"publicIp"`
	Port     int              `json:"port"`
	SNI      string           `json:"sni"`
	Ready    bool             `json:"ready"`
}
