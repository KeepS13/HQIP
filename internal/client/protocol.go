package client

// VLESS leases exist while streams are active, then release after an idle grace
// period. Missing idle leases do not indicate a broken client. Profile expiry,
// account revocation, core death and verified recovery still use their existing
// checks; the next stream must obtain a fresh server-side admission.
func idleProtocol(profile Profile, selected string) bool {
	for _, node := range profile.Nodes {
		if node.ID == selected {
			return node.Protocol == "vless"
		}
	}
	return false
}
