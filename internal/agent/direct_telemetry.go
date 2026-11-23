package agent

import (
	"context"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/outbound"
	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

// Registry ownership is directMu -> lease snapshot -> ledger.mu. No socket or
// admission call runs under directMu. Failed persistence retains closed meters;
// capacity stops new sessions rather than discard unreported server telemetry.
func (a *Agent) trackDirect(client outbound.Client) (outbound.Client, error) {
	a.directMu.Lock()
	if a.directSessions == nil {
		a.directSessions = make(map[outbound.Client]struct{})
	}
	if len(a.directSessions) >= maxLeaseQueueRows {
		a.directMu.Unlock()
		_ = client.Close()
		return nil, errors.New("direct telemetry queue full")
	}
	a.directSessions[client] = struct{}{}
	a.directMu.Unlock()
	return &trackedDirect{Client: client, agent: a}, nil
}

type trackedDirect struct {
	outbound.Client
	agent *Agent
}

func (c *trackedDirect) Close() error {
	err := c.Client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return errors.Join(err, c.agent.sampleDirect(ctx))
}

func (a *Agent) sampleDirect(ctx context.Context) error {
	a.directMu.Lock()
	defer a.directMu.Unlock()
	if len(a.directSessions) == 0 {
		return nil
	}
	clients := make([]outbound.Client, 0, len(a.directSessions))
	snapshots := make([]nodepolicy.Snapshot, 0, len(a.directSessions))
	for c := range a.directSessions {
		clients = append(clients, c)
		snapshots = append(snapshots, c.Snapshot())
	}
	up, down, err := a.ledger.recordDirect(ctx, a.now(), snapshots)
	if err != nil {
		return err
	}
	a.directPending.TX += up
	a.directPending.RX += down
	for i, s := range snapshots {
		if s.Closed {
			delete(a.directSessions, clients[i])
		}
	}
	return nil
}

// Called after HY2 sampling (including failures), under sampleMu. The raw rates
// include finals flushed between samples and do not apply subscription multipliers.
func (a *Agent) addDirectLive() {
	a.directMu.Lock()
	now := a.now()
	elapsed := now.Sub(a.directSampleAt).Seconds()
	up, down := float64(0), float64(0)
	if !a.directSampleAt.IsZero() && elapsed > 0 {
		up = float64(a.directPending.TX) / elapsed
		down = float64(a.directPending.RX) / elapsed
	}
	a.directPending = counter{}
	a.directSampleAt = now
	a.directMu.Unlock()
	a.liveMu.Lock()
	a.live.Metrics.UploadRate += up
	a.live.Metrics.DownloadRate += down
	a.liveMu.Unlock()
}

func (a *Agent) directOnline() map[string]int {
	a.directMu.Lock()
	defer a.directMu.Unlock()
	online := make(map[string]int)
	for c := range a.directSessions {
		s := c.Snapshot()
		if !s.Closed {
			online[s.AccountingID]++
		}
	}
	return online
}
