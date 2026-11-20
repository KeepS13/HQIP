package cloudflare

import "context"

// SwitchOwnedIPv4 preserves the name, ID and ownership token. Repeating a
// completed PUT is safe; a missing, foreign or externally edited record fails.
func (c *Client) SwitchOwnedIPv4(ctx context.Context, zone string, before Record, ip string) (Record, error) {
	if !ValidID(zone) || !ValidID(before.ID) || before.Type != "A" || before.Comment == "" || before.Proxied {
		return Record{}, ErrChanged
	}
	want := before
	want.Content = ip
	records, err := c.Records(ctx, zone, before.Name)
	if err != nil {
		return Record{}, err
	}
	if len(records) != 1 || records[0].ID != before.ID {
		return Record{}, ErrChanged
	}
	actual := records[0]
	if SameRecord(actual, want) {
		return actual, nil
	}
	if !SameRecord(actual, before) {
		return Record{}, ErrChanged
	}
	var updated Record
	if err = c.request(ctx, "PUT", "/zones/"+zone+"/dns_records/"+before.ID, want, &updated); err != nil {
		return Record{}, err
	}
	if updated.ID != before.ID || !SameRecord(updated, want) {
		return Record{}, ErrChanged
	}
	return updated, nil
}
