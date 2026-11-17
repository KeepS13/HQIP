package model

import "errors"

// SingBoxOutbound keeps desktop and remote exports on the same transport
// contract. Credentials must be scoped before calling; unsupported protocols
// fail closed instead of being interpreted as Hysteria passwords.
func (n ClientNode) SingBoxOutbound(tag string) (map[string]any, error) {
	if n.Server == "" || n.SNI == "" || n.Password == "" || n.Port < 1 || n.Port > 65535 {
		return nil, errors.New("invalid proxy endpoint")
	}
	if n.Protocol == "" || n.Protocol == "hysteria2" {
		return map[string]any{"type": "hysteria2", "tag": tag, "server": n.Server, "server_port": n.Port, "password": n.Password, "tls": map[string]any{"enabled": true, "server_name": n.SNI, "insecure": false}}, nil
	}
	if n.Protocol != "vless" || n.Reality == nil || n.Reality.Validate() != nil || n.Port != n.Reality.Port || n.SNI != n.Reality.ServerName {
		return nil, errors.New("unsupported or invalid proxy transport")
	}
	if _, err := RealityUUIDHashPrefix(n.Password); err != nil {
		return nil, err
	}
	return map[string]any{"type": "vless", "tag": tag, "server": n.Server, "server_port": n.Port, "uuid": n.Password, "packet_encoding": "xudp", "tls": map[string]any{"enabled": true, "server_name": n.SNI, "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}, "reality": map[string]any{"enabled": true, "public_key": n.Reality.PublicKey, "short_id": n.Reality.ShortID}}}, nil
}

func (n ClientNode) ClashProxy(name string) (map[string]any, error) {
	if _, err := n.SingBoxOutbound(name); err != nil {
		return nil, err
	}
	if n.Protocol != "vless" {
		return map[string]any{"name": name, "type": "hysteria2", "server": n.Server, "port": n.Port, "password": n.Password, "sni": n.SNI, "skip-cert-verify": false}, nil
	}
	return map[string]any{"name": name, "type": "vless", "server": n.Server, "port": n.Port, "uuid": n.Password, "network": "tcp", "tls": true, "servername": n.SNI, "client-fingerprint": "chrome", "udp": true, "packet-encoding": "xudp", "reality-opts": map[string]any{"public-key": n.Reality.PublicKey, "short-id": n.Reality.ShortID}}, nil
}
