package agent

import (
	"github.com/KeepS13/HQIP/internal/model"
	"time"
)

// Test-only hook to exercise the production gateway marker with local CAs.
func RealityBillingPasswordForTest(node, token, password, clientIP string) (string, error) {
	return (&Agent{cfg: model.AgentConfig{NodeID: node, Token: token}, now: time.Now}).realityBillingPassword(password, clientIP)
}
