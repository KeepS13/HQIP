//go:build with_utls

package reality

// VisionEnabled matches sing-vmess's uTLS adapter build tag. Release builds use
// this tag; the Agent advertises Vision only when that adapter is present.
const VisionEnabled = true
