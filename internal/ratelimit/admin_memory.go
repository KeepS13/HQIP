package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"
)

// MemoryAdminProtection is only for explicitly non-Redis local/development
// servers. A configured Redis implementation must never fall back to it.
type MemoryAdminProtection struct {
	mu      sync.Mutex
	now     func() time.Time
	sources map[string]*adminMemorySource
	global  []time.Time
}

type adminMemorySource struct {
	requests, failures []time.Time
	banUntil           time.Time
	banLevel           int
}

func NewMemoryAdminProtection(now func() time.Time) *MemoryAdminProtection {
	return &MemoryAdminProtection{now: now, sources: map[string]*adminMemorySource{}}
}

func keepAdminTimes(values []time.Time, cutoff time.Time) []time.Time {
	kept := values[:0]
	for _, at := range values {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	return kept
}

func (m *MemoryAdminProtection) apply(ctx context.Context, address, mode string) (AdminDecision, error) {
	if err := ctx.Err(); err != nil {
		return AdminDecision{}, err
	}
	key, err := AdminSource(address)
	if err != nil {
		return AdminDecision{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.global = keepAdminTimes(m.global, now.Add(-time.Minute))
	for source, entry := range m.sources {
		entry.requests = keepAdminTimes(entry.requests, now.Add(-15*time.Minute))
		entry.failures = keepAdminTimes(entry.failures, now.Add(-24*time.Hour))
		if !entry.banUntil.After(now) {
			entry.banLevel = 0
		}
		if len(entry.requests) == 0 && len(entry.failures) == 0 && entry.banLevel == 0 {
			delete(m.sources, source)
		}
	}
	entry := m.sources[key]
	if entry == nil {
		if mode == "success" {
			return AdminDecision{Allowed: true}, nil
		}
		if len(m.sources) >= 10000 {
			return AdminDecision{}, errors.New("管理员登录防护容量不足")
		}
		entry = &adminMemorySource{}
		m.sources[key] = entry
	}
	if mode == "success" {
		entry.failures = nil
		entry.banLevel = 0
		entry.banUntil = time.Time{}
		return AdminDecision{Allowed: true}, nil
	}
	started := false
	if mode == "failure" {
		entry.failures = append(entry.failures, now)
		level, duration := adminBan(len(entry.failures))
		if level > 0 && (entry.banLevel == 0 || level > entry.banLevel) {
			entry.banLevel, entry.banUntil = level, now.Add(duration)
			started = true
		}
	}
	if entry.banLevel > 0 {
		durations := []int{0, 60, 900, 3600}
		return AdminDecision{Reason: "temporary_ban", RetryAfter: durations[entry.banLevel], Failures: len(entry.failures), BanLevel: entry.banLevel, BanStarted: started}, nil
	}
	if mode == "failure" {
		return AdminDecision{Allowed: true, Failures: len(entry.failures)}, nil
	}
	if len(entry.requests) >= 10 {
		return AdminDecision{Reason: "source_rate", RetryAfter: 900}, nil
	}
	if len(m.global) >= 30 {
		return AdminDecision{Reason: "global_rate", RetryAfter: 60}, nil
	}
	entry.requests = append(entry.requests, now)
	m.global = append(m.global, now)
	return AdminDecision{Allowed: true}, nil
}

func (m *MemoryAdminProtection) AdminAdmit(ctx context.Context, address string) (AdminDecision, error) {
	return m.apply(ctx, address, "admit")
}

func (m *MemoryAdminProtection) AdminFailure(ctx context.Context, address string) (AdminDecision, error) {
	return m.apply(ctx, address, "failure")
}

func (m *MemoryAdminProtection) AdminSuccess(ctx context.Context, address string) error {
	_, err := m.apply(ctx, address, "success")
	return err
}
