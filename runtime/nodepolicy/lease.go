package nodepolicy

import (
	"context"
	"errors"
	"github.com/KeepS13/HQIP/runtime/nodepolicy/billing"
	"math"
	"sync"
	"time"
)

// connectionLease owns counters and schedules under mu; renewMu serializes
// renewal/final snapshots before acquiring mu. Socket callbacks must not call
// back into this lease. Unknown final delivery never returns delegated budget.
type connectionLease struct {
	billingSegments  []billing.Segment
	billingNormal    int64
	billingEmergency int64
	admission        *Admission
	ctx              context.Context
	cancel           context.CancelFunc
	closeConnection  func()
	mu               sync.Mutex
	renewMu          sync.Mutex
	grant            LeaseGrant
	deadline         time.Time // Contains monotonic clock; never recomputed from wall clock on read.
	usedUp, usedDown int64
	nextUpload       time.Time
	nextDownload     time.Time
	closed           bool
	wake             chan struct{}
	rateWake         chan struct{}
	finishOnce       sync.Once
	legacyPassword   string // Rechecked locally only, never sent centrally on renewal.
}

func legacyGrant(connectionID, accountingID string, start time.Time) LeaseGrant {
	return LeaseGrant{LeaseID: connectionID, UserID: "legacy", AccountingID: accountingID, ExpiresAt: start.Add(120 * time.Second), BudgetBytes: math.MaxInt64}
}

func (l *connectionLease) acceptGrant(g LeaseGrant, requestStart time.Time, initial bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.closed || g.Legacy || g.Authenticated || g.LeaseID == "" || g.AccountingID == "" || g.UserID == "" || g.BandwidthBytesPerSecond < 0 || g.BudgetBytes <= 0 || g.ConsumedBytes < 0 || g.ConsumedBytes > g.BudgetBytes || g.Revision < 0 {
		return errors.New("invalid lease grant")
	}
	if g.RateVersion < 0 || (!initial && !now.Before(l.deadline)) {
		return errors.New("invalid rate version or expired lease")
	}
	if !initial && (g.LeaseID != l.grant.LeaseID || g.AccountingID != l.grant.AccountingID || g.UserID != l.grant.UserID || g.Revision < l.grant.Revision || g.BudgetBytes < l.grant.BudgetBytes || g.ConsumedBytes > l.usedUp+l.usedDown || g.ConsumedBytes < l.grant.ConsumedBytes || g.RateVersion < l.grant.RateVersion) {
		return errors.New("non-monotonic lease grant")
	}
	if !initial && g.RateVersion == l.grant.RateVersion && (g.RatePending != l.grant.RatePending || g.BandwidthBytesPerSecond != l.grant.BandwidthBytesPerSecond) {
		return errors.New("rate changed without a new version")
	}
	if initial && g.ConsumedBytes != 0 {
		return errors.New("connection lease cannot be resumed")
	}
	if g.Billing != nil {
		if !g.Billing.Policy.Valid() || g.Billing.BudgetUnits < l.billingNormal || g.Billing.EmergencyBytes < l.billingEmergency {
			return errors.New("invalid billing grant")
		}
		if !initial && (l.grant.Billing == nil || g.Billing.Protocol != l.grant.Billing.Protocol || g.Billing.Version < l.grant.Billing.Version || g.Billing.BudgetUnits < l.grant.Billing.BudgetUnits) {
			return errors.New("non-monotonic billing grant")
		}
		if !initial && g.Billing.Version == l.grant.Billing.Version && g.Billing.Multiplier != l.grant.Billing.Multiplier {
			return errors.New("billing changed without version")
		}
		retained := l.billingSegments[:0]
		for _, segment := range l.billingSegments {
			confirmed := false
			for _, ack := range g.Billing.Acknowledged {
				if segment.Equal(ack) && segment.Version != l.grant.Billing.Version {
					confirmed = true
				}
			}
			if !confirmed {
				retained = append(retained, segment)
			}
		}
		l.billingSegments = retained
		if initial || g.Billing.Version != l.grant.Billing.Version {
			l.billingSegments = append(l.billingSegments, billing.Segment{Version: g.Billing.Version, AppliedAt: now.UTC()})
		}
	} else if !initial && l.grant.Billing != nil {
		return errors.New("billing authorization removed")
	}
	remaining := g.ExpiresAt.Sub(now)
	// Network time is included in the 120-second local authorization ceiling.
	ceiling := requestStart.Add(120 * time.Second).Sub(now)
	if remaining > ceiling {
		remaining = ceiling
	}
	if remaining <= 0 {
		return errors.New("lease expired")
	}
	l.deadline = now.Add(remaining)
	if g.Billing != nil {
		l.billingSegments[len(l.billingSegments)-1].ValidUntil = now.Add(remaining).UTC()
	}
	if g.RateVersion != l.grant.RateVersion || g.BandwidthBytesPerSecond != l.grant.BandwidthBytesPerSecond || g.RatePending != l.grant.RatePending {
		close(l.rateWake)
		l.rateWake = make(chan struct{})
		l.nextUpload, l.nextDownload = now, now
	}
	l.grant = g
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return nil
}
