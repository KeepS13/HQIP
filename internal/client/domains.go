package client

import "strings"

const DefaultServerURL = "https://api-hqip.sharevps.online"
const UserSiteURL = "https://hqip.sharevps.online"

const legacyServerURL = "https://api-hqip.5202345.xyz"
const legacyUserSiteURL = "https://hqip.5202345.xyz"

// Only known official roots move with the service. Paths, alternate ports and
// lookalike hosts are custom addresses and must never inherit official trust.
func migrateOfficialServerURL(server string) string {
	switch strings.TrimSuffix(server, "/") {
	case legacyServerURL, legacyUserSiteURL, DefaultServerURL, UserSiteURL:
		return DefaultServerURL
	default:
		return server
	}
}

func officialBypassDomains() []string {
	return []string{"api-hqip.sharevps.online", "hqip.sharevps.online", "api-hqip.5202345.xyz", "hqip.5202345.xyz"}
}
