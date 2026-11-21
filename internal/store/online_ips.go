package store

import (
	"database/sql"
	"time"
)

// Billing already owns the immutable protocol. Read it by lease primary key;
// do not introduce another device table or a second persisted protocol field.
// Queries using this fragment must alias portal_leases as l.
const onlineProtocolSQL = "COALESCE((SELECT json_extract(json_extract(state,'$.grant'),'$.protocol') FROM traffic_lease_billing WHERE lease_id=l.id),'')"

func onlineSlotKey(protocol, ip, lease string) string {
	if protocol == "vless" && ip != "" {
		return ip
	}
	return lease
}

// keepOnlineSlot is shared by revocation previews, actual revocation and rate
// allocation. Callers traverse leases oldest-first and exclude unusable nodes.
// HY2 instances each consume a slot; only VLESS shares one slot per source IP.
// Unknown legacy protocols/IPs retain separate lease identities.
func keepOnlineSlot(kept map[string]bool, protocol, ip, lease string, limit int) bool {
	key := onlineSlotKey(protocol, ip, lease)
	if kept[key] {
		return true
	}
	if len(kept) >= limit {
		return false
	}
	kept[key] = true
	return true
}

// onlineSlotAvailable runs inside the same subscription transaction as admission
// or slot reacquisition. Revoked peers still consume capacity, but cannot
// authorize replacements. An existing HY2 source never admits another instance
// at capacity; VLESS may share only another live, non-revoked VLESS source.
func onlineSlotAvailable(q portalQuery, subscription, protocol, ip string, occupied, limit int, now time.Time) (bool, error) {
	if occupied < limit {
		return true, nil
	}
	if protocol != "vless" || ip == "" {
		return false, nil
	}
	var count int
	err := q.QueryRow("SELECT COUNT(*) FROM portal_leases l WHERE l.subscription_id=? AND l.client_ip=? AND l.closed=0 AND l.revoked=0 AND l.expires>? AND "+onlineProtocolSQL+"='vless'", subscription, ip, now.Unix()).Scan(&count)
	return count > 0, err
}

// onlineSlotRetained applies the same oldest-slot ordering during renewal.
// It reads protocol from the ledger, never from the renewal's client payload.
func onlineSlotRetained(q *sql.Tx, subscription, lease string, limit int, now time.Time) (bool, error) {
	rows, err := q.Query("SELECT l.client_ip,l.id,"+onlineProtocolSQL+" FROM portal_leases l WHERE l.subscription_id=? AND l.closed=0 AND l.revoked=0 AND l.expires>? ORDER BY l.created_at,l.rowid", subscription, now.Unix())
	if err != nil {
		return false, err
	}
	defer rows.Close()
	kept := map[string]bool{}
	for rows.Next() {
		var address, id, protocol string
		if err := rows.Scan(&address, &id, &protocol); err != nil {
			return false, err
		}
		retained := keepOnlineSlot(kept, protocol, address, id, limit)
		if id == lease {
			return retained, nil
		}
	}
	return false, rows.Err()
}
