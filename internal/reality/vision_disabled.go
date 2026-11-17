//go:build !with_utls

package reality

// Untagged developer builds retain plain Reality compatibility but must never
// advertise or accept Vision without sing-vmess's uTLS adapter.
const VisionEnabled = false
