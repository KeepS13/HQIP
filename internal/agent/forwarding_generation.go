package agent

import (
	"context"

	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

// Caller holds offlineMu. Prefixing the existing child generation preserves
// old ledger semantics: pre-upgrade VLESS tunnels still belong to HY2. An Agent
// restart ends direct sessions, while a HY2 restart only ends HY2-owned children.
func (a *Agent) forwardingGeneration(ctx context.Context, transport ...string) (string, error) {
	if len(transport) == 1 && transport[0] == "vless-direct" {
		if a.realityGeneration == "" {
			a.realityGeneration = "vless:" + nodepolicy.NewConnectionID()
		}
		return a.realityGeneration, nil
	}
	return a.generation(ctx)
}
