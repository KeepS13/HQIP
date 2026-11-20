package netutil

import (
	"errors"
	"net/netip"
	"strings"
)

// Keep this policy aligned with admin-web/src/public-ipv4.ts. The shared
// testdata covers ordinary unicast addresses and special-purpose exclusions.
var nonPublicIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// ParsePublicIPv4 accepts canonical dotted-decimal IPv4 only, after trimming
// surrounding whitespace. It does not establish ownership or reachability.
func ParsePublicIPv4(value string) (string, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(value))
	if err == nil && a.Is4() {
		for _, prefix := range nonPublicIPv4 {
			if prefix.Contains(a) {
				return "", errors.New("请填写有效的公网 IPv4 地址")
			}
		}
		return a.String(), nil
	}
	return "", errors.New("请填写有效的公网 IPv4 地址")
}
