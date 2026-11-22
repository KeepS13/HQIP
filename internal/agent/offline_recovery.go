package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func sameOfflineIdentity(a, b model.OfflineGrant) bool {
	return a.ID == b.ID && a.NodeID == b.NodeID && a.CredentialHash == b.CredentialHash &&
		a.UserID == b.UserID && a.SubscriptionID == b.SubscriptionID && a.SessionID == b.SessionID &&
		a.ClientIP == b.ClientIP && a.Billing != nil && b.Billing != nil && a.Billing.Protocol == b.Billing.Protocol
}

// Initial receipts also remain useful after expiry/revocation: no child was
// delegated until this state was durable. Accept the receipt for settlement,
// while observe/localOfflineGrant separately decide current forwarding rights.
func initialOfflineState(g model.OfflineGrant, node, hash, protocol, ip string, now time.Time) (offlineState, error) {
	check := g
	check.Closed, check.Revoked = false, false
	if g.AuthorizedAt.After(now.Add(time.Minute)) || !validOfflineGrant(check, node, hash, g.AuthorizedAt) ||
		g.NodeQuota == nil || g.Billing == nil || g.Billing.Protocol != protocol || g.ClientIP != ip ||
		g.ConnectionSlotsVersion != model.ConnectionSlotsVersion || g.ReportVersion != 0 ||
		g.UploadBytes != 0 || g.DownloadBytes != 0 || g.EmergencyUsedBytes != 0 {
		return offlineState{}, &admissionProtocolError{}
	}
	s := offlineState{Grant: g, Children: map[string]offlineChild{}, Online: true, LastWall: now,
		Denied: g.Closed || g.Revoked || !now.Before(g.OfflineUntil)}
	if err := s.rememberBilling(); err != nil {
		return offlineState{}, err
	}
	return s, nil
}

// The caller holds offlineMu. Only a missing local parent with an untouched
// central receipt may be closed as unused. A saved parent can already own live
// children; a crash between saving it and deleting the request must preserve it.
func (a *Agent) recoverOfflineRequest(ctx context.Context, r offlineRequest, known map[string]offlineState) error {
	parts := strings.SplitN(r.Hash, ":", 3) // SplitN retains colons in IPv6 addresses.
	if len(parts) != 3 {
		return errors.New("invalid durable offline request identity")
	}
	status, data, err := a.requestAdmission(ctx, "/api/v1/agents/offline-grants/recover", map[string]string{"connectionId": r.ID})
	if err != nil {
		return err
	}
	if status == 404 {
		return nil // Unknown/in-flight request, or a control version without recovery.
	}
	if status != 200 {
		return errors.New("offline admission recovery unavailable")
	}
	var g model.OfflineGrant
	if json.Unmarshal(data, &g) != nil || g.NodeID != a.cfg.NodeID || g.CredentialHash != parts[1] ||
		g.Billing == nil || g.Billing.Protocol != parts[0] || g.ClientIP != parts[2] {
		return &admissionProtocolError{}
	}
	if s, ok := known[g.ID]; ok {
		if !sameOfflineIdentity(s.Grant, g) {
			return &admissionProtocolError{}
		}
	} else {
		s, err := initialOfflineState(g, a.cfg.NodeID, parts[1], parts[0], parts[2], a.now())
		if err != nil {
			return err // Reported usage without its local ledger must never become zero.
		}
		s.Denied = true // Background recovery settles; it never creates a new child.
		if err = a.ledger.saveOffline(s); err != nil {
			return err
		}
		known[g.ID] = s
	}
	return a.ledger.forgetOfflineRequest(r)
}

// Recover at most 16 requests every 30 seconds and use at most half of a sweep's
// WAN budget. An unreachable/missing receipt cannot starve existing child finals.
// Only scheduling is volatile; every uncertain request stays in SQLite.
func (a *Agent) recoverOfflineRequests(ctx context.Context) error {
	if a.now().Before(a.offlineRecoveryRetryAt) {
		return nil
	}
	a.offlineRecoveryRetryAt = a.now().Add(30 * time.Second)
	requests, err := a.ledger.offlineRequests(a.offlineRecoveryCursor)
	if err == nil && len(requests) == 0 && a.offlineRecoveryCursor != "" {
		a.offlineRecoveryCursor = ""
		requests, err = a.ledger.offlineRequests("")
	}
	if err != nil || len(requests) == 0 {
		return err
	}
	states, err := a.ledger.offlineStates()
	if err != nil {
		return err
	}
	known := make(map[string]offlineState, len(states))
	for _, s := range states {
		known[s.Grant.ID] = s
	}
	budget := admissionControlTimeout / 2
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)/2)
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var firstErr error
	for _, r := range requests {
		if ctx.Err() != nil {
			break
		}
		a.offlineRecoveryCursor = r.Hash
		if err := a.recoverOfflineRequest(ctx, r, known); firstErr == nil && err != nil {
			firstErr = err
		}
	}
	return firstErr
}
