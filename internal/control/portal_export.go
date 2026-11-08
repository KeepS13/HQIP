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
	case "clash", "v2ray":
		return values[0], nil
	default:
		return "", errors.New("unsupported subscription format")
	}
}

// v2rayN and v2rayNG accept a Base64-encoded list of standard Hysteria 2 URIs.
// These are the same subscription-scoped credentials used by the Clash export.
func portalV2RaySubscription(profile model.ClientProfile) ([]byte, error) {
	links := []string{}
	for _, node := range profile.Nodes {
		if !node.Ready {
			continue
		}
		if node.Server == "" || node.SNI == "" || node.Password == "" || node.Port < 1 || node.Port > 65535 {
			return nil, errors.New("invalid subscription node")
		}
		link := url.URL{
			Scheme:   "hysteria2",
			User:     url.User(node.Password),
			Host:     net.JoinHostPort(node.Server, strconv.Itoa(node.Port)),
			Path:     "/",
			RawQuery: url.Values{"sni": {node.SNI}, "insecure": {"0"}}.Encode(),
			Fragment: node.Name + " · " + node.ID,
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
