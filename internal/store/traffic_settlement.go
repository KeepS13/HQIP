package store

import (
	"database/sql"
	"github.com/apernet/hysteria/core/v2/billing"
)

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
	previousRemainder := remainder
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
	if nextRemainder := (deltaDownload%100 + remainder) % 100; nextRemainder != previousRemainder {
		if _, err = tx.Exec("UPDATE subscription_periods SET remainder=? WHERE id=?", nextRemainder, period); err != nil {
			return 0, 0, 0, err
		}
	}
	reserved := billing.Ceiling(max(int64(0), state.Grant.BudgetUnits-normalUnits))
	if state.NodeQuota != nil {
		// Node-owned quota has no central allocation to release. Its snapshot
		// watermarks account for these units after this transaction settles.
		reserved = 0
	}
	released := state.Reserved - reserved
	state.Reserved = reserved
	return up, down, released, nil
}
