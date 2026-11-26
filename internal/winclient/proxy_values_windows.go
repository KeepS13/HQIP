//go:build windows

package winclient

import (
	"fmt"
	"strings"

	"github.com/KeepS13/HQIP/internal/rules"
	"golang.org/x/sys/windows/registry"
)

// ownedProxyValues describes only settings owned by HQIP's proxy lease. Exact
// recovery hosts bypass the local core even when it is unavailable; the lease
// restores the user's previous exceptions/PAC without permanently replacing them.
func ownedProxyValues(port int) map[string]proxyValue {
	return map[string]proxyValue{
		"ProxyEnable":   {Present: true, Kind: registry.DWORD, Number: 1},
		"ProxyServer":   {Present: true, Kind: registry.SZ, Text: fmt.Sprintf("127.0.0.1:%d", port)},
		"ProxyOverride": {Present: true, Kind: registry.SZ, Text: "<local>;" + strings.Join(rules.OfficialServiceHosts(), ";")},
		"AutoConfigURL": {},
	}
}
