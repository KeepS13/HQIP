package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ClientChangeToken identifies this account's catalog/configuration, not its
// selected transport or traffic counters. Read-only, no credentials, new tables
// or per-node profile generation. Existing business revisions survive process
// restarts and work across control instances. Call before building a profile so
// a concurrent mutation is caught by the next notification, never acknowledged
// as already imported. ctx bounds database work for disconnected stream clients.
func (s *Store) ClientChangeToken(ctx context.Context, user string) (string, error) {
	h := sha256.New()
	var revision, enabled, rulesVersion int64
	if err := s.DB.QueryRowContext(ctx, `SELECT revision,enabled,(SELECT COALESCE(MAX(version),0) FROM rule_versions) FROM portal_users WHERE id=?`, user).Scan(&revision, &enabled, &rulesVersion); err != nil {
		return "", notFound(err)
	}
	fmt.Fprintf(h, "%s/%d/%d/%d\n", user, revision, enabled, rulesVersion)
	rows, err := s.DB.QueryContext(ctx, `SELECT s.id,s.revision,s.revoked,s.expires,a.node_ids,COALESCE((SELECT MAX(p.starts_at) FROM subscription_periods p WHERE p.subscription_id=s.id AND p.starts_at<=?),0)
FROM portal_subscriptions s JOIN subscription_allocations a ON a.subscription_id=s.id WHERE s.user_id=? ORDER BY s.id`, s.Now().Unix(), user)
	if err != nil {
		return "", err
	}
	nodes := map[string]bool{}
	for rows.Next() {
		var id, raw string
		var rev, revoked, expires, period int64
		if err = rows.Scan(&id, &rev, &revoked, &expires, &raw, &period); err != nil {
			break
		}
		// Expiry and a prepaid-period boundary are changes even without a write.
		fmt.Fprintf(h, "%s/%d/%d/%d/%t/%d/%s\n", id, rev, revoked, expires, expires > s.Now().Unix(), period, raw)
		var ids []string
		if err = json.Unmarshal([]byte(raw), &ids); err != nil {
			break
		}
		for _, node := range ids {
			nodes[node] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close() // Release the DB connection before the next bounded query.
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for start := 0; start < len(ids); start += 128 {
		batch := ids[start:min(start+128, len(ids))]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		// Config, protocol endpoint and capability changes matter; last_seen,
		// online counts, lease churn and current selection intentionally do not.
		query := `SELECT n.id,n.config,n.lifecycle,n.capabilities,COALESCE(m.value,'') FROM nodes n LEFT JOIN metadata m ON m.key='vless-reality:' || n.id WHERE n.id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + `) ORDER BY n.id`
		rows, err := s.DB.QueryContext(ctx, query, args...)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var id, config, lifecycle, caps, reality string
			if err = rows.Scan(&id, &config, &lifecycle, &caps, &reality); err != nil {
				break
			}
			data, _ := json.Marshal([]string{id, config, lifecycle, caps, reality})
			h.Write(data)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
