package client

import "strings"

// ShopURL follows the web portal's /catalog route. The native host selects the
// trusted account origin; presentation code cannot supply an arbitrary URL.
func ShopURL(server string) string {
	if migrateOfficialServerURL(server) == DefaultServerURL {
		return UserSiteURL + "/catalog"
	}
	return strings.TrimRight(server, "/") + "/catalog"
}
