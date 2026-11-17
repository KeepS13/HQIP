package model

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"strings"
)

const RealityCapability = "vless-reality-v1"

// Vision is negotiated separately so an unupgraded Agent never receives a
// client configuration it cannot serve. The base Reality API stays compatible.
const RealityVisionCapability = "vless-reality-vision-v1"
const RealityVisionFlow = "xtls-rprx-vision"

// DefaultRealityServerName is the TLS camouflage target, not the node's dial
// address. Users connect to their allocated node domain for both protocols.
const DefaultRealityServerName = "www.microsoft.com"

// AgentRealityConfig is installed by the owned Agent upgrade. No credential or
// key is supplied by the browser; the Agent creates its own persistent key.
type AgentRealityConfig struct {
	Port        int    `json:"port"`
	BackendPort int    `json:"backendPort"`
	ServerName  string `json:"serverName"`
}

// EndpointNodeID preserves infrastructure authorization when clients select a
// protocol endpoint. Node allocation continues to count physical nodes once.
func EndpointNodeID(id string) string { return strings.TrimSuffix(id, ":vless") }

// RealityEndpoint contains public connection parameters only. The private key
// belongs to the node and must never enter reports, profiles or subscriptions.
type RealityEndpoint struct {
	Port       int    `json:"port"`
	ServerName string `json:"serverName"`
	PublicKey  string `json:"publicKey"`
	ShortID    string `json:"shortId"`
	// Flow is derived by the controller from the Agent's capabilities. Agents
	// omit it on the wire so older controllers can still accept their reports.
	Flow string `json:"flow,omitempty"`
}

func (e RealityEndpoint) Validate() error {
	if e.Flow != "" && e.Flow != RealityVisionFlow {
		return errors.New("unsupported Reality flow")
	}
	key, err := base64.RawURLEncoding.DecodeString(e.PublicKey)
	sid, sidErr := hex.DecodeString(e.ShortID)
	if e.Port < 1 || e.Port > 65535 || err != nil || len(key) != 32 || sidErr != nil || len(sid) != 8 ||
		e.ServerName == "" || len(e.ServerName) > 253 || strings.ContainsAny(e.ServerName, ":/\\ \t\r\n") || net.ParseIP(e.ServerName) != nil {
		return errors.New("invalid Reality endpoint")
	}
	return nil
}

// RealityUUID is a 128-bit, protocol-compatible alias of the existing random
// per-subscription, per-node, per-scope credential. Rotation invalidates both
// protocols atomically; this alias does not create another quota or entitlement.
// Keep all 128 hash bits (UUID version bits are not required by VLESS).
func RealityUUID(password string) string {
	sum := sha256.Sum256([]byte(password))
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// RealityUUIDHashPrefix validates the canonical wire alias before it is used in
// a credential lookup. The original 256-bit password hash remains authoritative.
func RealityUUIDHashPrefix(id string) (string, error) {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return "", errors.New("invalid VLESS UUID")
	}
	h := strings.ReplaceAll(id, "-", "")
	raw, err := hex.DecodeString(h)
	if err != nil {
		return "", errors.New("invalid VLESS UUID")
	}
	// Stored credential hashes use raw URL-safe Base64. Only the first 21
	// characters (126 bits) are independent of the omitted hash suffix; the
	// 22nd character would also encode bits from byte 17 of the original hash.
	return base64.RawURLEncoding.EncodeToString(raw)[:21], nil
}
