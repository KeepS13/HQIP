package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Stable catalog notifications must keep a long-running connection authorized
// using the server's real deadline, while leaving ordinary heartbeats read-only.
func TestAccountChangesRenewExpiringGrantWithoutReconnecting(t *testing.T) {
	f := newRecoveryFixture(t)
	token := strings.Repeat("a", 64)
	f.mu.Lock()
	f.profile.ChangeToken = token
	f.profile.Subscription.ExpiresAt = f.now.Add(72 * time.Hour)
	f.mu.Unlock()
	until := enableOfflineFixture(t, f)
	c := f.c
	base := c.api.http.Transport
	var profiles atomic.Int32
	events := make(chan struct{})
	streamClosed := make(chan struct{})
	c.api.http.Transport = accountTransportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v3/client/changes" {
			if r.URL.Path == "/api/v3/client/profile" {
				profiles.Add(1)
			}
			return base.RoundTrip(r)
		}
		reader, writer := io.Pipe()
		go func() {
			defer close(streamClosed)
			defer writer.Close()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-events:
					if json.NewEncoder(writer).Encode(map[string]string{"changeToken": token}) != nil {
						return
					}
				}
			}
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: reader}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watchAccount(ctx, c.api)
	// Deliver a notification and await its post-sync contact marker. Real time
	// bounds the test; the authorization clock advances without a 24-hour wait.
	notify := func(at time.Time) {
		t.Helper()
		f.mu.Lock()
		f.now = at
		f.mu.Unlock()
		select {
		case events <- struct{}{}:
		case <-time.After(2 * time.Second):
			t.Fatal("stream did not read heartbeat")
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			c.mu.Lock()
			done := c.accountContact.Equal(at)
			c.mu.Unlock()
			if done {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("heartbeat was not processed")
	}
	f.e.mu.Lock()
	calls := len(f.e.calls)
	f.e.mu.Unlock()
	notify(until.Add(-10 * time.Minute))
	if profiles.Load() != 0 {
		t.Fatal("ordinary heartbeat fetched profile")
	}
	// An unchanged deadline is fetched once and then rate-limited, even when
	// notifications arrive repeatedly within the expiry window.
	notify(until.Add(-110 * time.Second))
	notify(until.Add(-100 * time.Second))
	if profiles.Load() != 1 {
		t.Fatal("authorization refresh did not respect one-minute interval", profiles.Load())
	}
	f.mu.Lock()
	renewed := until.Add(24 * time.Hour)
	for i := range f.profile.Nodes {
		f.profile.Nodes[i].OfflineUntil = renewed
	}
	f.mu.Unlock()
	notify(until.Add(-40 * time.Second))
	c.mu.Lock()
	actualUntil := c.saved.Offline.Until
	c.mu.Unlock()
	if profiles.Load() != 2 || !actualUntil.After(until) || actualUntil.After(renewed) {
		t.Fatal("did not import the real renewed grant", profiles.Load(), actualUntil)
	}
	notify(until.Add(5 * time.Second))
	c.accountWatchdogTick(ctx)
	f.e.mu.Lock()
	currentCalls := len(f.e.calls)
	f.e.mu.Unlock()
	if v := c.View(); v.RunState != "connected" || v.ActualNodeID != "A" || calls != currentCalls || profiles.Load() != 2 {
		t.Fatal("renewal stopped, rebuilt or repeatedly refreshed the connection", v.Status)
	}
	cancel()
	awaitClosedSignal(t, streamClosed, "stream remained open after cancellation")
}
