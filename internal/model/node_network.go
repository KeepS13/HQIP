package model

// LocalIP is the kernel source/listen address for both proxy protocols. It is
// persisted only in the node's private Agent config; the controller's public
// address, subscriptions, DNS and installation owner must never use this value.
// Older direct-IP configurations omit BindIP and keep their original behavior.
func (cfg AgentConfig) LocalIP() string {
	if cfg.BindIP != "" {
		return cfg.BindIP
	}
	return cfg.PublicIP
}
