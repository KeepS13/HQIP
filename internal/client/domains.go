package client

import "strings"

const DefaultServerURL = "https://api-hqip.sharevps.online"
const UserSiteURL = "https://hqip.sharevps.online"

// Only current official roots normalize to the official API. Retired roots,
// paths, alternate ports and lookalike hosts remain custom addresses.
func migrateOfficialServerURL(server string) string {
	switch strings.TrimSuffix(server, "/") {
	case DefaultServerURL, UserSiteURL:
		return DefaultServerURL
	default:
		return server
	}
}

func officialBypassDomains() []string {
	return []string{"api-hqip.sharevps.online", "hqip.sharevps.online"}
}
