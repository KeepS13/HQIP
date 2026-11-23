package nodepolicy

import (
	"context"
	"errors"

	"github.com/KeepS13/HQIP/runtime/nodepolicy/billing"
)

// Check authenticates an idle transport, without issuing a forwarding lease.
// An explicit endpoint prevents an older Agent from accidentally reserving a
// slot after ignoring an optional request field. Call Open before any target
// socket is opened; a successful check never authorizes payload or offline use.
func (a *Admission) Check(ctx context.Context, id, password, peerIP string) (string, error) {
	g, err := a.request(ctx, "/admission/check", map[string]string{
		"connectionId": id, "password": password, "clientIP": peerIP,
		"billingCapability": billing.Capability, "transport": a.config.Transport,
	})
	if err != nil {
		return "", err
	}
	if g.AccountingID == "" || g.LeaseID != "" || (!g.Authenticated && !g.Legacy) {
		return "", errors.New("invalid transport identity receipt")
	}
	return g.AccountingID, nil
}
