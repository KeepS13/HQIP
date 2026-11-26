package client

import (
	"strings"

	"github.com/KeepS13/HQIP/internal/rules"
)

const DefaultServerURL = "https://hqip.sharevps.online"
const UserSiteURL = "https://hqip.sharevps.online"
const previousOfficialServerURL = "https://api-hqip.sharevps.online"

// Migrate only the exact previous official API root. Other retired roots,
// paths, alternate ports and lookalike hosts remain custom addresses.
func migrateOfficialServerURL(server string) string {
	switch strings.TrimSuffix(server, "/") {
	case DefaultServerURL, previousOfficialServerURL:
		return DefaultServerURL
	default:
		return server
	}
}

func officialBypassDomains() []string {
	return rules.OfficialServiceHosts()
}
