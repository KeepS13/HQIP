package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

// Only compiled into tests. Portable process fixtures have no systemd; sampling,
// admission and synchronization remain the actual production implementations.
func NewPortalE2EAgentForTest(t testing.TB, cfg model.AgentConfig) (*Agent, error) {
	a, e := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		return nil, e
	}
	a.generation = func(context.Context) (string, error) { return strings.Repeat("a", 32), nil }
	a.restartCore = func(context.Context) error {
		t.Error("portal operation attempted whole-node restart")
		return fmt.Errorf("test prohibits node restart")
	}
	return a, nil
}
