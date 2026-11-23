package nodepolicy

import (
	"context"
	"errors"
	"github.com/KeepS13/HQIP/runtime/nodepolicy/billing"
	"net/url"
	"time"
)

func (l *connectionLease) fail() {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		l.cancel()
		l.closeConnection()
	}
	l.mu.Unlock()
}

func (l *connectionLease) renew() error {
	l.renewMu.Lock()
	defer l.renewMu.Unlock()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errors.New("closed lease")
	}
	id := l.grant.LeaseID
	usage := leaseUsage{UploadBytes: l.usedUp, DownloadBytes: l.usedDown, AppliedRateVersion: l.grant.RateVersion, Billing: append([]billing.Segment(nil), l.billingSegments...)}
	deadline := l.deadline
	l.mu.Unlock()
	if !time.Now().Before(deadline) {
		l.fail()
		return errors.New("lease expired")
	}
	requestCtx, cancel := context.WithDeadline(l.ctx, deadline)
	defer cancel()
	start := time.Now()
	var g LeaseGrant
	var err error
	if l.legacyPassword != "" {
		g, err = l.admission.request(requestCtx, "/admission", map[string]string{"connectionId": id, "password": l.legacyPassword, "legacyRenewal": "true"})
		if err == nil {
			if !g.Legacy || g.AccountingID != l.grant.AccountingID || g.LeaseID != "" {
				l.fail()
				return errors.New("legacy authorization ended")
			}
			g = legacyGrant(id, g.AccountingID, start)
		}
	} else {
		g, err = l.admission.request(requestCtx, "/leases/"+url.PathEscape(id)+"/renew", usage)
	}
	if err != nil {
		var ae admissionError
		if errors.As(err, &ae) && ae.denied {
			l.fail()
		}
		return err
	}
	if err = l.acceptGrant(g, start, false); err != nil {
		l.fail()
		return err
	}
	return nil
}

func (l *connectionLease) run() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		l.mu.Lock()
		remaining := time.Until(l.deadline)
		l.mu.Unlock()
		if remaining <= 0 {
			l.fail()
			return
		}
		timer := time.NewTimer(remaining)
		select {
		case <-l.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			l.fail()
			return
		case <-l.wake:
			timer.Stop()
		case <-ticker.C:
			timer.Stop()
			_ = l.renew()
		}
	}
}

func (l *connectionLease) finish() {
	if l == nil {
		return
	}
	l.finishOnce.Do(func() {
		l.fail()
		if l.legacyPassword != "" {
			return
		}
		l.renewMu.Lock()
		defer l.renewMu.Unlock()
		l.mu.Lock()
		id := l.grant.LeaseID
		usage := leaseUsage{UploadBytes: l.usedUp, DownloadBytes: l.usedDown, Closed: true, AppliedRateVersion: l.grant.RateVersion, Billing: append([]billing.Segment(nil), l.billingSegments...)}
		l.mu.Unlock()
		// The Agent durably queues a received final before central delivery.
		// No reservation may be reused if the Agent never received this report.
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_, _ = l.admission.request(ctx, "/leases/"+url.PathEscape(id)+"/renew", usage)
	})
}
