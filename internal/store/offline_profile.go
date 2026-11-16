package store

import (
	"github.com/KeepS13/HQIP/internal/model"
	"time"
)

func (s *Store) clientOfflinePolicy(n *model.ClientNode, subscription, scope string) error {
	var capabilities string
	if err := s.DB.QueryRow("SELECT capabilities FROM nodes WHERE id=?", n.ID).Scan(&capabilities); err != nil {
		return err
	}
	var until int64
	if err := s.DB.QueryRow(`SELECT COALESCE(MAX(l.expires),0) FROM portal_leases l JOIN portal_offline_grants o ON o.lease_id=l.id WHERE l.node_id=? AND l.subscription_id=? AND l.session_id=? AND l.closed=0 AND l.revoked=0 AND l.expires>?`, n.ID, subscription, scope, s.Now().Unix()).Scan(&until); err != nil {
		return err
	}
	if until != 0 {
		n.OfflineUntil = time.Unix(until, 0).UTC()
	}
	if containsCapability(capabilities, "offline-admission-v1") && (s.OfflineAdmissionEnabled(n.ID) || until != 0) {
		n.OfflineAdmissionVersion = model.OfflineAdmissionVersion
	}
	return nil
}
