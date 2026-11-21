package store

import "github.com/KeepS13/HQIP/internal/model"

// The missing IP is intentional only in historical fixtures. HTTP and public
// Store admission APIs require a canonical address for every new connection.
func (s *Store) legacyProtocolLease(node, connection, password, protocol string, offline bool) (model.ConnectionLease, error) {
	if protocol != "hysteria2" && protocol != "vless" {
		return model.ConnectionLease{}, ErrConflict
	}
	return s.acquireLease(node, connection, password, protocol, "", offline)
}

func (s *Store) legacyNodeQuotaGrant(node, connection, password, protocol string) (model.OfflineGrant, error) {
	if protocol != "hysteria2" && protocol != "vless" {
		return model.OfflineGrant{}, ErrConflict
	}
	l, err := s.acquireLease(node, connection, password, protocol, "", true, true)
	if err != nil {
		return model.OfflineGrant{}, err
	}
	return loadOfflineGrant(s.DB, node, l.ID)
}

// Test-only constructors reproduce pre-IP grants to verify migration and late
// usage settlement. Production HTTP admission cannot issue these old grants.
func (s *Store) AcquireLease(node, connection, password string, offline ...bool) (model.ConnectionLease, error) {
	return s.acquireLease(node, connection, password, "", "", len(offline) == 1 && offline[0])
}

func (s *Store) AcquireOfflineGrant(node, connection, password string) (model.OfflineGrant, error) {
	l, err := s.AcquireLease(node, connection, password, true)
	if err != nil {
		return model.OfflineGrant{}, err
	}
	return loadOfflineGrant(s.DB, node, l.ID)
}

func (s *Store) AcquireProtocolOfflineGrant(node, connection, password, protocol string) (model.OfflineGrant, error) {
	lease, err := s.legacyProtocolLease(node, connection, password, protocol, true)
	if err != nil {
		return model.OfflineGrant{}, err
	}
	return loadOfflineGrant(s.DB, node, lease.ID)
}
