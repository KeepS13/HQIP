package nodepolicy

import (
	"errors"
	"math"
	"time"
)

// consume paces each direction independently and shares the cumulative budget.
// A failed write is charged conservatively; no payload exceeds the reserved cap.
func (l *connectionLease) consume(upload bool, n int) error {
	if l == nil || n == 0 {
		return nil
	}
	if n < 0 {
		return errors.New("negative payload")
	}
	for n > 0 {
		chunk := n
		if chunk > 16<<10 {
			chunk = 16 << 10
		}
		l.mu.Lock()
		rate := l.grant.BandwidthBytesPerSecond
		l.mu.Unlock()
		if rate > 0 {
			pacedChunk := rate / 10
			if pacedChunk < 1 {
				pacedChunk = 1
			}
			if int64(chunk) > pacedChunk {
				chunk = int(pacedChunk)
			}
		}
		if err := l.consumeChunk(upload, int64(chunk)); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

func (l *connectionLease) consumeChunk(upload bool, n int64) error {
	for {
		l.mu.Lock()
		if l.closed || !time.Now().Before(l.deadline) {
			l.mu.Unlock()
			l.fail()
			return errors.New("lease expired or closed")
		}
		if l.grant.RatePending {
			wake := l.rateWake
			deadline := l.deadline
			l.mu.Unlock()
			timer := time.NewTimer(time.Until(deadline))
			select {
			case <-l.ctx.Done():
				timer.Stop()
				return errors.New("lease closed")
			case <-wake:
				timer.Stop()
				continue
			case <-timer.C:
				continue
			}
		}
		if rate := l.grant.BandwidthBytesPerSecond; rate > 0 {
			chunk := rate / 10
			if chunk < 1 {
				chunk = 1
			}
			if n > chunk {
				l.mu.Unlock()
				return l.consume(upload, int(n))
			}
		}
		if n > l.grant.BudgetBytes-l.usedUp-l.usedDown || !l.billingAllows(n) {
			l.mu.Unlock()
			if err := l.renew(); err != nil {
				return err
			}
			l.mu.Lock()
			enough := n <= l.grant.BudgetBytes-l.usedUp-l.usedDown && l.billingAllows(n)
			l.mu.Unlock()
			if !enough {
				l.fail()
				return errors.New("lease budget exhausted")
			}
			continue
		}
		rate := l.grant.BandwidthBytesPerSecond
		now := time.Now()
		wait := time.Duration(0)
		wake := l.rateWake
		rateVersion := l.grant.RateVersion
		if rate > 0 {
			next := &l.nextDownload
			if upload {
				next = &l.nextUpload
			}
			if next.Before(now) {
				*next = now
			}
			duration := time.Duration(math.Ceil(float64(n) * float64(time.Second) / float64(rate)))
			if duration < 0 || duration > 120*time.Second {
				l.mu.Unlock()
				return errors.New("payload rate exceeds lease window")
			}
			*next = next.Add(duration)
			wait = next.Sub(now)
		}
		deadline := l.deadline
		l.mu.Unlock()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-l.ctx.Done():
				timer.Stop()
				return errors.New("lease closed")
			case <-wake:
				timer.Stop()
				continue
			case <-timer.C:
			}
		}
		l.mu.Lock()
		if l.grant.RateVersion != rateVersion || l.grant.RatePending {
			l.mu.Unlock()
			continue
		}
		if l.closed || !time.Now().Before(l.deadline) || !time.Now().Before(deadline) {
			l.mu.Unlock()
			l.fail()
			return errors.New("lease expired")
		}
		if n > l.grant.BudgetBytes-l.usedUp-l.usedDown || !l.billingAllows(n) {
			l.mu.Unlock()
			continue
		}
		l.chargeBilling(upload, n)
		if upload {
			l.usedUp += n
		} else {
			l.usedDown += n
		}
		l.mu.Unlock()
		return nil
	}
}
