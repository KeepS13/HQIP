package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

type leaseBilling struct {
	Grant         billing.Grant             `json:"grant"`
	Policies      map[int64]billing.Policy  `json:"policies"`
	Segments      map[int64]billing.Segment `json:"segments"`
	Reserved      int64                     `json:"reserved"`
	NormalUsed    int64                     `json:"normalUsed"`
	EmergencyUsed int64                     `json:"emergencyUsed"`
}

func readTrafficBilling(query portalQuery) (model.TrafficBillingSettings, error) {
	var settings model.TrafficBillingSettings
	err := query.QueryRow("SELECT enabled,revision,hy2,vless FROM traffic_billing_settings WHERE id=1").Scan(&settings.Enabled, &settings.Revision, &settings.Hysteria2, &settings.VLESS)
	settings.History = []model.TrafficBillingChange{}
	return settings, err
}

func (s *Store) TrafficBillingSettings() (model.TrafficBillingSettings, error) {
	settings, err := readTrafficBilling(s.DB)
	if err != nil {
		return settings, err
	}
	rows, err := s.DB.Query("SELECT revision,actor,at,before_config,after_config FROM traffic_billing_changes ORDER BY revision DESC LIMIT 100")
	if err != nil {
		return settings, err
	}
	for rows.Next() {
		var change model.TrafficBillingChange
		var before, after string
		var stamp int64
		if err = rows.Scan(&change.Revision, &change.Actor, &stamp, &before, &after); err == nil {
			err = json.Unmarshal([]byte(before), &change.Before)
		}
		if err == nil {
			err = json.Unmarshal([]byte(after), &change.After)
		}
		if err != nil {
			rows.Close()
			return settings, err
		}
		change.At = time.Unix(stamp, 0).UTC()
		settings.History = append(settings.History, change)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return settings, err
	}
	rows, err = s.DB.Query("SELECT COALESCE(b.state,'') FROM portal_leases l LEFT JOIN traffic_lease_billing b ON b.lease_id=l.id WHERE l.closed=0 AND l.revoked=0 AND l.expires>?", s.Now().Unix())
	if err != nil {
		return settings, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return settings, err
		}
		settings.ActiveLeases++
		var state leaseBilling
		if raw == "" {
			if settings.Enabled {
				settings.PendingLeases++
			}
			continue
		}
		if err = json.Unmarshal([]byte(raw), &state); err != nil {
			return settings, err
		}
		matched := false
		for version, segment := range state.Segments {
			if version == settings.Revision && !segment.AppliedAt.IsZero() {
				matched = true
			}
		}
		if !matched {
			settings.PendingLeases++
		}
	}
	return settings, rows.Err()
}

func (s *Store) SaveTrafficBillingSettings(input model.TrafficBillingInput, actor string) (model.TrafficBillingSettings, error) {
	if input.Hysteria2 < 1 || input.Hysteria2 > 1000 || input.VLESS < 1 || input.VLESS > 1000 {
		return model.TrafficBillingSettings{}, errors.New("倍率必须在 0.01～10.00 之间，最多两位小数")
	}
	err := retryDatabaseError(func() error {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		previous, err := readTrafficBilling(tx)
		if err != nil {
			return err
		}
		if previous.Revision != input.ExpectedRevision {
			return ErrConflict
		}
		if input.Enabled {
			rows, err := tx.Query("SELECT id,capabilities FROM nodes WHERE lifecycle='active' AND json_extract(config,'$.status')='ready'")
			if err != nil {
				return err
			}
			for rows.Next() {
				var id, raw string
				var capabilities []string
				if err = rows.Scan(&id, &raw); err != nil {
					rows.Close()
					return err
				}
				if json.Unmarshal([]byte(raw), &capabilities) != nil || !slices.Contains(capabilities, billing.Capability) {
					rows.Close()
					return fmt.Errorf("%w: 节点 %s 的 Agent 或内核尚未支持流量计费，请先完成升级", ErrConflict, id)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		result, err := tx.Exec("UPDATE traffic_billing_settings SET enabled=?,revision=revision+1,hy2=?,vless=? WHERE id=1 AND revision=?", input.Enabled, input.Hysteria2, input.VLESS, input.ExpectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		before := model.TrafficBillingInput{Enabled: previous.Enabled, ExpectedRevision: previous.Revision, Hysteria2: previous.Hysteria2, VLESS: previous.VLESS}
		if _, err = tx.Exec("INSERT INTO traffic_billing_changes(revision,actor,at,before_config,after_config) VALUES(?,?,?,?,?)", previous.Revision+1, actor, s.Now().Unix(), jsonString(before), jsonString(input)); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return model.TrafficBillingSettings{}, err
	}
	return s.TrafficBillingSettings()
}

func billingPolicy(query portalQuery, protocol string) (billing.Policy, error) {
	settings, err := readTrafficBilling(query)
	if err != nil {
		return billing.Policy{}, err
	}
	policy := billing.Policy{Version: settings.Revision, Multiplier: 100, Protocol: protocol}
	if settings.Enabled {
		if protocol == "hysteria2" {
			policy.Multiplier = settings.Hysteria2
		} else {
			policy.Multiplier = settings.VLESS
		}
	}
	if !policy.Valid() {
		return policy, ErrConflict
	}
	return policy, nil
}

func loadLeaseBilling(query portalQuery, id string) (*leaseBilling, error) {
	var raw string
	err := query.QueryRow("SELECT state FROM traffic_lease_billing WHERE lease_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state leaseBilling
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	if !state.Grant.Policy.Valid() || state.Policies == nil || state.Segments == nil {
		return nil, ErrConflict
	}
	return &state, nil
}

func saveLeaseBilling(tx *sql.Tx, id string, state *leaseBilling) error {
	_, err := tx.Exec("INSERT INTO traffic_lease_billing(lease_id,state,remaining_units,remaining_emergency) VALUES(?,?,?,?) ON CONFLICT(lease_id) DO UPDATE SET state=excluded.state,remaining_units=excluded.remaining_units,remaining_emergency=excluded.remaining_emergency", id, jsonString(state), max(int64(0), state.Grant.BudgetUnits-state.NormalUsed), max(int64(0), state.Grant.EmergencyBytes-state.EmergencyUsed))
	return err
}

func createLeaseBilling(tx *sql.Tx, id, protocol string, budget int64) (*leaseBilling, error) {
	if protocol == "" {
		return nil, nil
	}
	policy, err := billingPolicy(tx, protocol)
	if err != nil {
		return nil, err
	}
	units, err := billing.Units(budget, 100)
	if err != nil {
		return nil, err
	}
	state := &leaseBilling{Grant: billing.Grant{Policy: policy, BudgetUnits: units}, Policies: map[int64]billing.Policy{policy.Version: policy}, Segments: map[int64]billing.Segment{}, Reserved: budget}
	return state, saveLeaseBilling(tx, id, state)
}

func settleBilling(tx *sql.Tx, subscription, period string, state *leaseBilling, segments []billing.Segment, upload, download, emergency int64) (int64, int64, int64, error) {
	if len(segments) > 256 {
		return 0, 0, 0, ErrConflict
	}
	oldUnits := state.Grant.ConsumedUnits
	var deltaUpload, deltaDownload int64
	seen := map[int64]bool{}
	for _, segment := range segments {
		policy, ok := state.Policies[segment.Version]
		if !ok || seen[segment.Version] || segment.Validate() != nil {
			return 0, 0, 0, ErrConflict
		}
		seen[segment.Version] = true
		previous := state.Segments[segment.Version]
		if segment.Upload < previous.Upload || segment.Download < previous.Download || segment.EmergencyUpload < previous.EmergencyUpload || segment.EmergencyDownload < previous.EmergencyDownload || (!previous.AppliedAt.IsZero() && !segment.AppliedAt.Equal(previous.AppliedAt)) {
			return 0, 0, 0, ErrConflict
		}
		up, err := billing.Units(segment.Upload-previous.Upload, policy.Multiplier)
		if err != nil {
			return 0, 0, 0, err
		}
		down, err := billing.Units(segment.Download-previous.Download, policy.Multiplier)
		if err != nil {
			return 0, 0, 0, err
		}
		if deltaUpload, err = billing.Add(deltaUpload, up); err != nil {
			return 0, 0, 0, err
		}
		if deltaDownload, err = billing.Add(deltaDownload, down); err != nil {
			return 0, 0, 0, err
		}
		state.Segments[segment.Version] = segment
	}
	var totalUpload, totalDownload, totalEmergency, normalUnits int64
	for version, segment := range state.Segments {
		var err error
		if totalUpload, err = billing.Add(totalUpload, segment.Upload); err != nil {
			return 0, 0, 0, err
		}
		if totalDownload, err = billing.Add(totalDownload, segment.Download); err != nil {
			return 0, 0, 0, err
		}
		if totalEmergency, err = billing.Add(totalEmergency, segment.EmergencyUpload+segment.EmergencyDownload); err != nil {
			return 0, 0, 0, err
		}
		units, err := billing.Units(segment.Upload-segment.EmergencyUpload+segment.Download-segment.EmergencyDownload, state.Policies[version].Multiplier)
		if err != nil {
			return 0, 0, 0, err
		}
		if normalUnits, err = billing.Add(normalUnits, units); err != nil {
			return 0, 0, 0, err
		}
	}
	if totalUpload != upload || totalDownload != download || totalEmergency != emergency || normalUnits > state.Grant.BudgetUnits || emergency > state.Grant.EmergencyBytes {
		return 0, 0, 0, ErrConflict
	}
	added, err := billing.Add(deltaUpload, deltaDownload)
	if err != nil {
		return 0, 0, 0, err
	}
	if state.Grant.ConsumedUnits, err = billing.Add(oldUnits, added); err != nil {
		return 0, 0, 0, err
	}
	state.NormalUsed, state.EmergencyUsed = normalUnits, emergency
	state.Grant.Acknowledged = append([]billing.Segment(nil), segments...)
	if _, err = tx.Exec("INSERT INTO traffic_billing_remainders(subscription_id) VALUES(?) ON CONFLICT(subscription_id) DO NOTHING", subscription); err != nil {
		return 0, 0, 0, err
	}
	var remainder int64
	if err = tx.QueryRow("SELECT remainder FROM subscription_periods WHERE id=?", period).Scan(&remainder); err != nil {
		return 0, 0, 0, err
	}
	// One period remainder across both directions: splitting a byte
	// between upload/download must not change total billed usage.
	up := deltaUpload/100 + (deltaUpload%100+remainder)/100
	remainder = (deltaUpload%100 + remainder) % 100
	down := deltaDownload/100 + (deltaDownload%100+remainder)/100
	var usedUp, usedDown int64
	if err = tx.QueryRow("SELECT upload,download FROM portal_usage WHERE subscription_id=?", subscription).Scan(&usedUp, &usedDown); err != nil {
		return 0, 0, 0, err
	}
	if usedUp, err = billing.Add(usedUp, up); err != nil {
		return 0, 0, 0, err
	}
	if usedDown, err = billing.Add(usedDown, down); err != nil {
		return 0, 0, 0, err
	}
	if _, err = billing.Add(usedUp, usedDown); err != nil {
		return 0, 0, 0, err
	}
	if _, err = tx.Exec("UPDATE subscription_periods SET remainder=? WHERE id=?", (deltaDownload%100+remainder)%100, period); err != nil {
		return 0, 0, 0, err
	}
	reserved := billing.Ceiling(max(int64(0), state.Grant.BudgetUnits-normalUnits))
	released := state.Reserved - reserved
	state.Reserved = reserved
	return up, down, released, nil
}

func issueBillingPolicy(tx *sql.Tx, state *leaseBilling) error {
	policy, err := billingPolicy(tx, state.Grant.Protocol)
	if err != nil {
		return err
	}
	state.Grant.Policy = policy
	state.Policies[policy.Version] = policy
	return nil
}

func validBillingReplay(state *leaseBilling, segments []billing.Segment) bool {
	if state == nil {
		return len(segments) == 0
	}
	seen := map[int64]bool{}
	for _, segment := range segments {
		previous := state.Segments[segment.Version]
		if seen[segment.Version] || !previous.Equal(segment) {
			return false
		}
		seen[segment.Version] = true
	}
	return true
}

func (s *Store) BillingDisplay(user, session, node, protocol string) ([]model.BillingDisplay, error) {
	rows, err := s.DB.Query("SELECT l.id,l.subscription_id,l.expires,b.state FROM portal_leases l JOIN traffic_lease_billing b ON b.lease_id=l.id WHERE l.user_id=? AND l.session_id=? AND l.node_id=? AND l.closed=0 AND l.revoked=0 AND l.expires>?", user, session, node, s.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.BillingDisplay{}
	for rows.Next() {
		var id, subscription, raw string
		var expiry int64
		if err = rows.Scan(&id, &subscription, &expiry, &raw); err != nil {
			return nil, err
		}
		var state leaseBilling
		if err = json.Unmarshal([]byte(raw), &state); err != nil {
			return nil, err
		}
		if state.Grant.Protocol != protocol {
			continue
		}
		for version, segment := range state.Segments {
			if segment.ValidUntil.IsZero() {
				continue
			}
			until := segment.ValidUntil
			if until.After(time.Unix(expiry, 0)) {
				until = time.Unix(expiry, 0).UTC()
			}
			result = append(result, model.BillingDisplay{Policy: state.Policies[version], LeaseID: id, NodeID: node, SubscriptionID: subscription, AppliedAt: segment.AppliedAt, ValidUntil: until})
		}
	}
	return result, rows.Err()
}
