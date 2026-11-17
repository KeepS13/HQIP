package control

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
)

// The existing URL remains a Clash subscription unless its format is explicit.
// Reject duplicate or malformed format parameters instead of guessing a client.
func portalExportFormat(rawQuery string) (string, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", err
	}
	values, present := query["format"]
	if !present {
		return "clash", nil
	}
	if len(values) != 1 {
		return "", errors.New("ambiguous subscription format")
	}
	switch values[0] {
	case "clash", "v2ray", "singbox":
		return values[0], nil
	default:
		return "", errors.New("unsupported subscription format")
	}
}

// Each physical node exports its ready protocol endpoints. Both formats share
// the same subscription-scoped authorization and rotate together.
func portalV2RaySubscription(profile model.ClientProfile) ([]byte, error) {
	links := []string{}
	for _, node := range profile.Nodes {
		if !node.Ready {
			continue
		}
		if node.Server == "" || node.SNI == "" || node.Password == "" || node.Port < 1 || node.Port > 65535 {
			return nil, errors.New("invalid subscription node")
		}
		// HY2 uses its certificate domain even for older profiles containing an
		// IP address. Reality's SNI is a separate camouflage target, so its URI
		// must use Server (the allocated node domain) as the dial address.
		link := url.URL{
			Scheme:   "hysteria2",
			User:     url.User(node.Password),
			Host:     net.JoinHostPort(node.SNI, strconv.Itoa(node.Port)),
			Path:     "/",
			RawQuery: url.Values{"sni": {node.SNI}, "insecure": {"0"}}.Encode(),
			Fragment: node.Name,
		}
		if _, err := node.SingBoxOutbound(node.ID); err != nil {
			return nil, err
		}
		if node.Protocol == "vless" {
			link.Scheme, link.Host, link.Path = "vless", net.JoinHostPort(node.Server, strconv.Itoa(node.Port)), ""
			link.RawQuery = url.Values{"encryption": {"none"}, "security": {"reality"}, "type": {"tcp"}, "sni": {node.SNI}, "fp": {"chrome"}, "pbk": {node.Reality.PublicKey}, "sid": {node.Reality.ShortID}}.Encode()
		}
		if node.ID == profile.DefaultNodeID {
			links = append([]string{link.String()}, links...)
		} else {
			links = append(links, link.String())
		}
	}
	if len(links) == 0 {
		return nil, errors.New("no ready subscription nodes")
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))), nil
}
