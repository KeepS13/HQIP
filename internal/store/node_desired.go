package store

import (
	"database/sql"
	"github.com/KeepS13/HQIP/internal/model"
)

func (s *Store) Desired(id string) (model.AgentDesired, error) {
	var result model.AgentDesired
	result.PortalEnabled = s.PortalEnabled()
	if s.OfflineAdmissionEnabled(id) {
		result.OfflineAdmissionVersion = model.OfflineAdmissionVersion
		result.NodeQuotaVersion = 1
	}
	result.Devices = []model.AgentDevice{}
	var lifecycle string
	err := s.DB.QueryRow("SELECT desired_revision,lifecycle FROM nodes WHERE id=?", id).Scan(&result.Revision, &lifecycle)
	if err != nil {
		return result, notFound(err)
	}
	var retirement model.AgentRetirement
	err = s.DB.QueryRow("SELECT id,stopped FROM node_retirements WHERE node_id=?", id).Scan(&retirement.ID, &retirement.Finalize)
	if err == nil {
		result.Retirement = &retirement
	} else if err != sql.ErrNoRows {
		return result, err
	}
	rows, err := s.DB.Query(`SELECT c.device_id,c.password,
CASE WHEN ?!='active' THEN 0 WHEN c.device_id=? THEN 1 WHEN d.id IS NULL OR d.deleted_at>0 OR d.enabled=0 THEN 0 WHEN a.node_id IS NOT NULL THEN 1 WHEN r.id IS NOT NULL AND r.immediate=0 AND r.status!='revoked' THEN 1 ELSE 0 END,
COALESCE(r.id,''),CASE WHEN r.id IS NOT NULL AND r.immediate=0 AND r.status!='revoked' THEN 60 ELSE 0 END
FROM credentials c LEFT JOIN devices d ON d.id=c.device_id
LEFT JOIN assignments a ON a.device_id=c.device_id AND a.node_id=c.node_id
LEFT JOIN removals r ON r.id=(SELECT rr.id FROM removals rr WHERE rr.device_id=c.device_id AND rr.node_id=c.node_id AND rr.status!='cancelled' ORDER BY rr.created_at DESC,rr.rowid DESC LIMIT 1) AND a.node_id IS NULL
WHERE c.node_id=? ORDER BY c.device_id`, lifecycle, ProbeID(id), id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var d model.AgentDevice
		var enc string
		if err = rows.Scan(&d.ID, &enc, &d.Enabled, &d.RemovalID, &d.RevokeAfterSeconds); err != nil {
			return result, err
		}
		d.Password, err = s.Vault.Decrypt(enc)
		if err != nil {
			return result, secretError(err)
		}
		result.Devices = append(result.Devices, d)
		if result.PortalEnabled && d.ID != ProbeID(id) {
			result.Devices[len(result.Devices)-1].Enabled = false
			result.Devices[len(result.Devices)-1].RevokeAfterSeconds = 0
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if lifecycle == "active" {
		var mode string
		if err = s.DB.QueryRow("SELECT COALESCE(json_extract(config,'$.certificateMode'),'http') FROM nodes WHERE id=?", id).Scan(&mode); err != nil {
			return result, err
		}
		if mode == "cloudflare" {
			result.Certificate, err = s.CertificateBundle(id)
		}
	}
	return result, err
}
