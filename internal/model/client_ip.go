package model

import (
	"errors"
	"net/netip"
)

// CanonicalClientIP accepts an address observed by a trusted proxy ingress, not
// a socket address or a client-supplied header. Unmap prevents one IPv4 peer
// from occupying two identities through IPv4-mapped IPv6 notation.
func CanonicalClientIP(raw string) (string, error) {
	ip, err := netip.ParseAddr(raw)
	zone := ip.Zone()
	ip = ip.Unmap()
	if err != nil || zone != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return "", errors.New("invalid client IP")
	}
	return ip.String(), nil
}

// Version 3 counts HY2 instances plus VLESS source IPs. Version 2 grouped both
// protocols by IP; version 1 introduced explicit slot release/reacquisition.
const ConnectionSlotsVersion = 3
