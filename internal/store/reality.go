package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// recordReality stores only the node's public endpoint as business metadata in
// the same transaction as its capability/health report. An older Agent or an
// unavailable ingress clears it, so exports cannot advertise a missing service.
func recordReality(tx *sql.Tx, node string, report model.AgentReport, active bool) error {
	key := "vless-reality:" + node
	capable := false
	vision := false
	for _, capability := range report.Capabilities {
		capable = capable || capability == model.RealityCapability
		vision = vision || capability == model.RealityVisionCapability
	}
	if report.Reality == nil || !capable || !active {
		_, err := tx.Exec("DELETE FROM metadata WHERE key=?", key)
		return err
	}
	if err := report.Reality.Validate(); err != nil {
		return err
	}
	// Copy caller-owned data. A capability removal must clear the flow as well
	// as an upgrade enables it, including a node rollback with an unchanged key.
	endpoint := *report.Reality
	endpoint.Flow = ""
	if vision {
		endpoint.Flow = model.RealityVisionFlow
	}
	if _, err := tx.Exec("INSERT INTO metadata(key,value) VALUES('vless_reality_version','1') ON CONFLICT(key) DO UPDATE SET value='1'"); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, jsonString(endpoint))
	return err
}

func (s *Store) RealityEndpoint(node string) (*model.RealityEndpoint, error) {
	var raw string
	err := s.DB.QueryRow("SELECT value FROM metadata WHERE key=?", "vless-reality:"+node).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var endpoint model.RealityEndpoint
	if err = json.Unmarshal([]byte(raw), &endpoint); err != nil {
		return nil, err
	}
	if err = endpoint.Validate(); err != nil {
		return nil, err
	}
	return &endpoint, nil
}

// expandProtocolNodes expands presentation endpoints after physical-node
// selection. Allocation, credentials and quota are still owned by the base node.
func (s *Store) expandProtocolNodes(nodes []model.ClientNode) ([]model.ClientNode, error) {
	result := make([]model.ClientNode, 0, len(nodes)*2)
	for _, node := range nodes {
		endpoint, err := s.RealityEndpoint(node.ID)
		if err != nil {
			return nil, err
		}
		if endpoint == nil {
			result = append(result, node)
			continue
		}
		vless := node
		node.Protocol, node.NodeID = "hysteria2", node.ID
		node.Name += " · Hysteria 2"
		vless.ID, vless.NodeID, vless.Protocol = node.ID+":vless", node.ID, "vless"
		vless.Name += " · VLESS + Reality"
		vless.Port, vless.SNI, vless.Reality = endpoint.Port, endpoint.ServerName, endpoint
		if vless.Password != "" {
			vless.Password = model.RealityUUID(vless.Password)
		}
		// Existing sessions retain their local protocol-independent leases. A new VLESS
		// tunnel needs alias resolution; do not promise cold offline reconnect.
		vless.OfflineAdmissionVersion, vless.OfflineUntil = 0, time.Time{}
		result = append(result, node, vless)
	}
	return result, nil
}

// ResolveRealityCredential is only called for the authenticated Agent's own
// node. The UUID aliases an existing credential; it grants no independent access.
// The shared local Agent performs the authoritative transactional admission,
// including recovery holds, revocation, quota, concurrent slots and offline policy.
func (s *Store) ResolveRealityCredential(ctx context.Context, node, uuid string) (string, error) {
	prefix, err := model.RealityUUIDHashPrefix(uuid)
	if err != nil {
		return "", ErrUnauthorized
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT password_secret FROM portal_credentials WHERE node_id=? AND substr(password_hash,1,21)=? LIMIT 2", node, prefix)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	secrets := []string{}
	for rows.Next() {
		var secret string
		if err = rows.Scan(&secret); err != nil {
			return "", err
		}
		secrets = append(secrets, secret)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	// Refuse even a hypothetical alias collision; never select an arbitrary scope.
	if len(secrets) != 1 {
		return "", ErrUnauthorized
	}
	password, err := s.Vault.Decrypt(secrets[0])
	if err != nil {
		return "", err
	}
	if model.RealityUUID(password) != uuid {
		return "", ErrUnauthorized
	}
	return password, nil
}
