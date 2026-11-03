package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/certificates"
	"github.com/KeepS13/HQIP/internal/cloudflare"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func EnsureCloudflareSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS cloudflare_settings(id INTEGER PRIMARY KEY CHECK(id=1),token TEXT NOT NULL,zone_id TEXT NOT NULL,zone_name TEXT NOT NULL,email TEXT NOT NULL,version INTEGER NOT NULL,updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS cloudflare_dns(id TEXT PRIMARY KEY,node_id TEXT NOT NULL REFERENCES nodes(id),kind TEXT NOT NULL,zone_id TEXT NOT NULL,zone_name TEXT NOT NULL,record TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',created_at INTEGER NOT NULL,error TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX IF NOT EXISTS node_dns_active ON cloudflare_dns(node_id) WHERE kind='A' AND state!='deleted';
CREATE TABLE IF NOT EXISTS certificate_accounts(directory TEXT PRIMARY KEY,private_key TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS certificate_versions(node_id TEXT NOT NULL REFERENCES nodes(id),version INTEGER NOT NULL,bundle TEXT NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(node_id,version));
CREATE TABLE IF NOT EXISTS certificate_state(node_id TEXT PRIMARY KEY REFERENCES nodes(id),domain TEXT NOT NULL DEFAULT '',issued_version INTEGER NOT NULL DEFAULT 0,applied_version INTEGER NOT NULL DEFAULT 0,not_before INTEGER NOT NULL DEFAULT 0,not_after INTEGER NOT NULL DEFAULT 0,next_attempt INTEGER NOT NULL DEFAULT 0,failures INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',applied_error TEXT NOT NULL DEFAULT '',reported_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS cloudflare_leases(name TEXT PRIMARY KEY,owner TEXT NOT NULL,expires INTEGER NOT NULL);
`)
	return err
}

type CloudflareSettings struct {
	Configured      bool      `json:"configured"`
	DefaultZoneID   string    `json:"defaultZoneId"`
	DefaultZoneName string    `json:"defaultZoneName"`
	Email           string    `json:"email"`
	Version         int64     `json:"version"`
	UpdatedAt       time.Time `json:"updatedAt"`
}
type CloudflareSettingsInput struct {
	Token           string `json:"token"`
	DefaultZoneID   string `json:"defaultZoneId"`
	Email           string `json:"email"`
	ExpectedVersion int64  `json:"expectedVersion"`
}

func (s *Store) CloudflareSettings() (CloudflareSettings, error) {
	var out CloudflareSettings
	var at int64
	err := s.DB.QueryRow("SELECT zone_id,zone_name,email,version,updated_at FROM cloudflare_settings WHERE id=1").Scan(&out.DefaultZoneID, &out.DefaultZoneName, &out.Email, &out.Version, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Configured = true
	out.UpdatedAt = time.Unix(at, 0).UTC()
	return out, nil
}
func (s *Store) CloudflareToken() (string, error) {
	var encrypted string
	err := s.DB.QueryRow("SELECT token FROM cloudflare_settings WHERE id=1").Scan(&encrypted)
	if err != nil {
		return "", notFound(err)
	}
	return s.Vault.Decrypt(encrypted)
}
func (s *Store) SaveCloudflareSettings(in CloudflareSettingsInput, zone cloudflare.Zone) (CloudflareSettings, error) {
	var out CloudflareSettings
	if in.DefaultZoneID != zone.ID || zone.Status != "active" || !cloudflare.ValidID(zone.ID) {
		return out, errors.New("请选择可用的 Cloudflare 域名")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var version int64
	var encrypted string
	err = tx.QueryRow("SELECT token,version FROM cloudflare_settings WHERE id=1").Scan(&encrypted, &version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if in.ExpectedVersion != version {
		return out, ErrConflict
	}
	if in.Token != "" {
		if err = cloudflare.ValidateToken(in.Token); err != nil {
			return out, err
		}
		encrypted, err = s.Vault.Encrypt(in.Token)
		if err != nil {
			return out, err
		}
	}
	if encrypted == "" {
		return out, errors.New("请先配置 Cloudflare Token")
	}
	_, err = tx.Exec("INSERT INTO cloudflare_settings(id,token,zone_id,zone_name,email,version,updated_at) VALUES(1,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET token=excluded.token,zone_id=excluded.zone_id,zone_name=excluded.zone_name,email=excluded.email,version=excluded.version,updated_at=excluded.updated_at", encrypted, zone.ID, zone.Name, in.Email, version+1, s.Now().Unix())
	if err != nil {
		return out, err
	}
	// A verified settings repair can fix a revoked Token or wrong Zone. Allow
	// the next scheduled/manual attempt immediately, without discarding the
	// last error or an already issued certificate.
	if _, err = tx.Exec("UPDATE certificate_state SET failures=0,next_attempt=? WHERE failures>0", s.Now().Unix()); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	s.Audit("cloudflare.settings", zone.ID)
	return s.CloudflareSettings()
}

type DNSIntent struct {
	ID        string            `json:"id"`
	NodeID    string            `json:"nodeId"`
	Kind      string            `json:"kind"`
	ZoneID    string            `json:"zoneId"`
	ZoneName  string            `json:"zoneName"`
	Record    cloudflare.Record `json:"record"`
	State     string            `json:"state"`
	CreatedAt time.Time         `json:"createdAt"`
	Error     string            `json:"error"`
}

// DNSStatus deliberately omits challenge values and ownership secrets. Deleted
// node records remain visible until their resource cleanup has completed.
type DNSStatus struct {
	ID            string `json:"id"`
	NodeID        string `json:"nodeId"`
	Domain        string `json:"domain"`
	IP            string `json:"ip"`
	ZoneName      string `json:"zoneName"`
	State         string `json:"state"`
	Error         string `json:"error"`
	NodeLifecycle string `json:"nodeLifecycle"`
}

func (s *Store) DNSStatuses() ([]DNSStatus, error) {
	rows, err := s.DB.Query("SELECT d.id,d.node_id,json_extract(d.record,'$.name'),json_extract(d.record,'$.content'),d.zone_name,d.state,d.error,n.lifecycle FROM cloudflare_dns d JOIN nodes n ON n.id=d.node_id WHERE d.kind='A' AND d.state!='deleted' ORDER BY d.created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DNSStatus{}
	for rows.Next() {
		var item DNSStatus
		if err = rows.Scan(&item.ID, &item.NodeID, &item.Domain, &item.IP, &item.ZoneName, &item.State, &item.Error, &item.NodeLifecycle); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) DeletedCloudflareNodes() ([]model.Node, error) {
	rows, err := s.DB.Query("SELECT n.id FROM nodes n WHERE n.lifecycle='deleted' AND (EXISTS(SELECT 1 FROM cloudflare_dns d WHERE d.node_id=n.id AND d.state!='deleted') OR EXISTS(SELECT 1 FROM certificate_state c WHERE c.node_id=n.id))")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.Node{}
	for _, id := range ids {
		node, e := s.Node(id)
		if e != nil {
			return nil, e
		}
		out = append(out, node)
	}
	return out, nil
}

func (s *Store) DNSIntent(id string) (DNSIntent, error) {
	var out DNSIntent
	var raw string
	var at int64
	err := s.DB.QueryRow("SELECT id,node_id,kind,zone_id,zone_name,record,state,created_at,error FROM cloudflare_dns WHERE id=?", id).Scan(&out.ID, &out.NodeID, &out.Kind, &out.ZoneID, &out.ZoneName, &raw, &out.State, &at, &out.Error)
	if err != nil {
		return out, notFound(err)
	}
	err = json.Unmarshal([]byte(raw), &out.Record)
	out.CreatedAt = time.Unix(at, 0).UTC()
	return out, err
}
func (s *Store) NodeDNSIntent(nodeID string) (DNSIntent, error) {
	var id string
	err := s.DB.QueryRow("SELECT id FROM cloudflare_dns WHERE node_id=? AND kind='A' AND state!='deleted'", nodeID).Scan(&id)
	if err != nil {
		return DNSIntent{}, notFound(err)
	}
	return s.DNSIntent(id)
}
func (s *Store) NodeDNSIntents(nodeID string) ([]DNSIntent, error) {
	rows, err := s.DB.Query("SELECT id FROM cloudflare_dns WHERE node_id=? AND state!='deleted'", nodeID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []DNSIntent{}
	for _, id := range ids {
		intent, e := s.DNSIntent(id)
		if e != nil {
			return nil, e
		}
		out = append(out, intent)
	}
	return out, nil
}
func (s *Store) CreateDNSIntent(nodeID, zoneID, zoneName string, record cloudflare.Record) (DNSIntent, error) {
	out := DNSIntent{ID: NewID(), NodeID: nodeID, Kind: record.Type, ZoneID: zoneID, ZoneName: zoneName, Record: record, State: "pending", CreatedAt: s.Now().UTC()}
	result, err := s.DB.Exec("INSERT INTO cloudflare_dns(id,node_id,kind,zone_id,zone_name,record,created_at) SELECT ?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM nodes WHERE id=? AND lifecycle='active' AND deleted_at=0)", out.ID, nodeID, record.Type, zoneID, zoneName, jsonString(record), s.Now().Unix(), nodeID)
	if err == nil {
		n, _ := result.RowsAffected()
		if n != 1 {
			err = ErrConflict
		}
	}
	return out, err
}
func (s *Store) CompleteDNSIntent(id string, record cloudflare.Record) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw, nodeID, kind string
	if err = tx.QueryRow("SELECT record,node_id,kind FROM cloudflare_dns WHERE id=? AND state IN ('pending','active','error')", id).Scan(&raw, &nodeID, &kind); err != nil {
		return notFound(err)
	}
	var expected cloudflare.Record
	if err = json.Unmarshal([]byte(raw), &expected); err != nil {
		return err
	}
	if !cloudflare.SameRecord(expected, record) || !cloudflare.ValidID(record.ID) {
		return cloudflare.ErrChanged
	}
	if _, err = tx.Exec("UPDATE cloudflare_dns SET record=?,state='active',error='' WHERE id=?", jsonString(record), id); err != nil {
		return err
	}
	if kind == "A" {
		res, e := tx.Exec("UPDATE nodes SET config=json_set(config,'$.domain',?,'$.certificateMode','cloudflare','$.zoneId',(SELECT zone_id FROM cloudflare_dns WHERE id=?)) WHERE id=? AND lifecycle='active' AND deleted_at=0", record.Name, id, nodeID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrConflict
		}
	}
	return tx.Commit()
}
func (s *Store) DNSIntentState(id, state, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := s.DB.Exec("UPDATE cloudflare_dns SET state=?,error=? WHERE id=?", state, message, id)
	return err
}
func (s *Store) PendingDNSCleanup() ([]DNSIntent, error) {
	rows, err := s.DB.Query("SELECT id FROM cloudflare_dns WHERE state='cleanup' OR (kind='TXT' AND state!='deleted' AND created_at<?)", s.Now().Add(-30*time.Minute).Unix())
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []DNSIntent{}
	for _, id := range ids {
		entry, e := s.DNSIntent(id)
		if e != nil {
			return nil, e
		}
		out = append(out, entry)
	}
	return out, nil
}
func (s *Store) AcquireCloudflareLease(name string, ttl time.Duration) (string, error) {
	token := secure.Token()
	res, err := s.DB.Exec("INSERT INTO cloudflare_leases(name,owner,expires) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE cloudflare_leases.expires<=?", name, token, s.Now().Add(ttl).Unix(), s.Now().Unix())
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return "", ErrConflict
	}
	return token, nil
}
func (s *Store) ReleaseCloudflareLease(name, token string) {
	_, _ = s.DB.Exec("DELETE FROM cloudflare_leases WHERE name=? AND owner=?", name, token)
}
func (s *Store) CertificateAccountKey(directory string) (string, error) {
	var encrypted string
	err := s.DB.QueryRow("SELECT private_key FROM certificate_accounts WHERE directory=?", directory).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		key, e := certificates.NewAccountKey()
		if e != nil {
			return "", e
		}
		encrypted, e = s.Vault.Encrypt(key)
		if e != nil {
			return "", e
		}
		if _, e = s.DB.Exec("INSERT OR IGNORE INTO certificate_accounts(directory,private_key) VALUES(?,?)", directory, encrypted); e != nil {
			return "", e
		}
		err = s.DB.QueryRow("SELECT private_key FROM certificate_accounts WHERE directory=?", directory).Scan(&encrypted)
	}
	if err != nil {
		return "", err
	}
	return s.Vault.Decrypt(encrypted)
}
func (s *Store) CertificateBundle(nodeID string) (*model.CertificateBundle, error) {
	var encrypted string
	err := s.DB.QueryRow("SELECT v.bundle FROM certificate_versions v JOIN certificate_state s ON s.node_id=v.node_id AND s.issued_version=v.version WHERE v.node_id=?", nodeID).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err := s.Vault.Decrypt(encrypted)
	if err != nil {
		return nil, err
	}
	var bundle model.CertificateBundle
	if err = json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, err
	}
	return &bundle, nil
}
func (s *Store) PublishCertificate(nodeID string, bundle *model.CertificateBundle) error {
	leaf, err := certificates.Validate(bundle, s.Now())
	if err != nil {
		return err
	}
	bundle.NotBefore = leaf.NotBefore
	bundle.NotAfter = leaf.NotAfter
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var domain string
	if err = tx.QueryRow("SELECT json_extract(config,'$.domain') FROM nodes WHERE id=? AND lifecycle='active' AND deleted_at=0", nodeID).Scan(&domain); err != nil {
		return notFound(err)
	}
	if domain != bundle.Domain {
		return errors.New("节点域名已经变化，已拒绝发布证书")
	}
	var version int64
	err = tx.QueryRow("SELECT COALESCE(MAX(version),0) FROM certificate_versions WHERE node_id=?", nodeID).Scan(&version)
	if err != nil {
		return err
	}
	bundle.Version = version + 1
	encrypted, err := s.Vault.Encrypt(jsonString(bundle))
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO certificate_versions(node_id,version,bundle,created_at) VALUES(?,?,?,?)", nodeID, bundle.Version, encrypted, s.Now().Unix()); err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO certificate_state(node_id,domain,issued_version,not_before,not_after,next_attempt) VALUES(?,?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET domain=excluded.domain,issued_version=excluded.issued_version,not_before=excluded.not_before,not_after=excluded.not_after,next_attempt=excluded.next_attempt,failures=0,error=''", nodeID, bundle.Domain, bundle.Version, bundle.NotBefore.Unix(), bundle.NotAfter.Unix(), certificates.RenewalTime(bundle.NotBefore, bundle.NotAfter).Unix())
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE nodes SET desired_revision=desired_revision+1 WHERE id=?", nodeID); err != nil {
		return err
	}
	return tx.Commit()
}

type NodeCertificateStatus struct {
	Mode           string    `json:"mode"`
	NodeID         string    `json:"nodeId"`
	Domain         string    `json:"domain"`
	IssuedVersion  int64     `json:"issuedVersion"`
	AppliedVersion int64     `json:"appliedVersion"`
	NotAfter       time.Time `json:"notAfter"`
	NextAttempt    time.Time `json:"nextAttempt"`
	Error          string    `json:"error"`
	AppliedError   string    `json:"appliedError"`
	ExpiresInDays  int       `json:"expiresInDays"`
	Warning        string    `json:"warning"`
}

func (s *Store) CertificateStatus(nodeID string) (NodeCertificateStatus, error) {
	var status NodeCertificateStatus
	var after, next int64
	err := s.DB.QueryRow("SELECT c.node_id,c.domain,c.issued_version,c.applied_version,c.not_after,c.next_attempt,c.error,c.applied_error,COALESCE(json_extract(n.config,'$.certificateMode'),'http') FROM certificate_state c JOIN nodes n ON n.id=c.node_id WHERE c.node_id=?", nodeID).Scan(&status.NodeID, &status.Domain, &status.IssuedVersion, &status.AppliedVersion, &after, &next, &status.Error, &status.AppliedError, &status.Mode)
	if err != nil {
		return status, notFound(err)
	}
	if after > 0 {
		status.NotAfter = time.Unix(after, 0).UTC()
		status.ExpiresInDays = int(math.Ceil(status.NotAfter.Sub(s.Now()).Hours() / 24))
		status.Warning = certificates.ExpiryWarning(s.Now(), status.NotAfter)
	}
	if next > 0 {
		status.NextAttempt = time.Unix(next, 0).UTC()
	}
	return status, nil
}
func (s *Store) CertificateStatuses() ([]NodeCertificateStatus, error) {
	rows, err := s.DB.Query("SELECT node_id FROM certificate_state ORDER BY node_id")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []NodeCertificateStatus{}
	for _, id := range ids {
		status, e := s.CertificateStatus(id)
		if e != nil {
			return nil, e
		}
		out = append(out, status)
	}
	return out, nil
}
func (s *Store) CertificateFailure(nodeID, domain, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}
	var failures int
	err := s.DB.QueryRow("SELECT failures FROM certificate_state WHERE node_id=?", nodeID).Scan(&failures)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	failures++
	_, err = s.DB.Exec("INSERT INTO certificate_state(node_id,domain,failures,next_attempt,error) VALUES(?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET failures=excluded.failures,next_attempt=excluded.next_attempt,error=excluded.error", nodeID, domain, failures, s.Now().Add(certificates.RetryDelay(failures)).Unix(), message)
	return err
}
func (s *Store) RecordCertificateStatus(nodeID string, status model.CertificateStatus) error {
	var mode string
	if err := s.DB.QueryRow("SELECT COALESCE(json_extract(config,'$.certificateMode'),'http') FROM nodes WHERE id=?", nodeID).Scan(&mode); err != nil {
		return notFound(err)
	}
	// During a queued mode change the candidate process can report before the
	// deployment has committed. Do not display it as applied after a rollback.
	if mode != "cloudflare" && status.Version > 0 || mode == "cloudflare" && status.Version == 0 && status.Error == "" {
		return nil
	}
	// Agent text is untrusted. Do not persist PEM or arbitrary error strings.
	message := ""
	if status.Error != "" {
		message = "节点证书应用失败，请检查节点日志"
	}
	if status.Version == 0 && mode != "cloudflare" {
		if status.NotAfter.IsZero() {
			return nil
		}
		_, err := s.DB.Exec("INSERT INTO certificate_state(node_id,domain,not_after,applied_error,reported_at) SELECT id,json_extract(config,'$.domain'),?,?,? FROM nodes WHERE id=? AND COALESCE(json_extract(config,'$.certificateMode'),'http')!='cloudflare' ON CONFLICT(node_id) DO UPDATE SET not_after=excluded.not_after,applied_version=0,applied_error=excluded.applied_error,reported_at=excluded.reported_at", status.NotAfter.Unix(), message, s.Now().Unix(), nodeID)
		return err
	}
	bundle, err := s.CertificateBundle(nodeID)
	if err != nil {
		return err
	}
	if bundle == nil || status.Version > bundle.Version || status.Version < 0 {
		return errors.New("节点回报的证书版本无效")
	}
	if message != "" {
		// A failed renewal retains the old applied version (and a first apply
		// failure has version zero). Surface its error without claiming that
		// the newly issued version was installed.
		_, err = s.DB.Exec("UPDATE certificate_state SET applied_error=?,reported_at=? WHERE node_id=? AND EXISTS(SELECT 1 FROM nodes n WHERE n.id=node_id AND json_extract(n.config,'$.certificateMode')='cloudflare')", message, s.Now().Unix(), nodeID)
		return err
	}
	if status.Version != bundle.Version {
		return nil
	}
	if message == "" && !status.NotAfter.Equal(bundle.NotAfter) {
		return errors.New("节点回报的证书有效期不匹配")
	}
	_, err = s.DB.Exec("UPDATE certificate_state SET applied_version=CASE WHEN ?='' THEN MAX(applied_version,?) ELSE applied_version END,applied_error=?,reported_at=? WHERE node_id=? AND EXISTS(SELECT 1 FROM nodes n WHERE n.id=node_id AND json_extract(n.config,'$.certificateMode')='cloudflare')", message, status.Version, message, s.Now().Unix(), nodeID)
	return err
}
func (s *Store) ClearNodeCertificates(nodeID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM certificate_versions WHERE node_id=?", nodeID); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM certificate_state WHERE node_id=?", nodeID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CertificateRenewalDue(nodeID string) bool {
	var next int64
	err := s.DB.QueryRow("SELECT next_attempt FROM certificate_state WHERE node_id=?", nodeID).Scan(&next)
	return errors.Is(err, sql.ErrNoRows) || (err == nil && next <= s.Now().Unix())
}
func (s *Store) CloudflareName(nodeID, zoneName string) string {
	return "n-" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(secure.Hash(nodeID + secure.Token())[:16], "_", "a"), "-", "b")) + "." + zoneName
}
