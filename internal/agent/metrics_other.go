//go:build !linux

package agent

import "github.com/KeepS13/HQIP/internal/model"

// Supported node operating systems are Linux; the portable fallback permits
// unit tests and tooling to run on the administrator's Windows computer.
type systemMetrics struct{}

func (m *systemMetrics) read(statePath string) model.Metrics { return model.Metrics{} }
