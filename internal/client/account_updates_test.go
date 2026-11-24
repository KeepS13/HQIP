package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Reuse the real profile/rules fixture. Only the optional notification stream
// is simulated, with independent traffic counts and cancellation observation.
func TestAccountChangesImportOnlyChangedCatalog(t *testing.T) {
	c, _, _ := setupClient(t)
	first, next := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var token atomic.Value
	token.Store(first)
	c.mu.Lock()
	c.saved.Profile.ChangeToken = first
	c.mu.Unlock()
	base := c.api.http.Transport
	var profiles, requests atomic.Int32
	streamStarted, streamClosed := make(chan struct{}), make(chan struct{})
	events := make(chan string, 16)
	c.api.http.Transport = accountTransportFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Path == "/api/v3/client/changes" {
			reader, writer := io.Pipe()
			close(streamStarted)
			go func() {
				defer close(streamClosed)
				defer writer.Close()
				for {
					select {
					case <-r.Context().Done():
						return
					case value := <-events:
						if err := json.NewEncoder(writer).Encode(map[string]string{"changeToken": value}); err != nil {
							return
						}
					}
				}
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: reader}, nil
		}
		resp, err := base.RoundTrip(r)
		if err == nil && r.URL.Path == "/api/v3/client/profile" {
			profiles.Add(1)
			var p Profile
			if err = json.NewDecoder(resp.Body).Decode(&p); err != nil {
				return nil, err
			}
			resp.Body.Close()
			p.ChangeToken = token.Load().(string)
			data, _ := json.Marshal(p)
			resp.Body = io.NopCloser(strings.NewReader(string(data)))
		}
		return resp, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	awaitClosedSignal(t, streamStarted, "change stream did not start")
	for i := 0; i < 8; i++ {
		events <- first
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		seen := !c.accountContact.IsZero()
		c.mu.Unlock()
		if seen {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if profiles.Load() != 0 || requests.Load() != 1 {
		t.Fatal("login/unchanged heartbeat repeated import", profiles.Load(), requests.Load())
	}
	if err := c.SelectSubscriptionNode("sub-a", "B"); err != nil {
		t.Fatal(err)
	}
	token.Store(next)
	for i := 0; i < 8; i++ {
		events <- next
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		applied := c.saved.Profile.ChangeToken == next
		c.mu.Unlock()
		if applied {
			break
		}
		time.Sleep(time.Millisecond)
	}
	c.Close()
	awaitClosedSignal(t, streamClosed, "close leaked a stream")
	if profiles.Load() != 1 || requests.Load() != 2 {
		t.Fatal("one business change caused duplicate imports/reports", profiles.Load(), requests.Load())
	}
	if c.View().DesiredNodeID != "B" {
		t.Fatal("business refresh replaced local choice")
	}
}

func TestIdleLegacyRunHasNoPeriodicAccountRequests(t *testing.T) {
	c, _, _ := setupClient(t)
	transport := &selectionTransport{base: c.api.http.Transport}
	c.api.http.Transport = transport
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	// Cross the former five-second polling boundary, including 20 local clicks.
	for i := 0; i < 20; i++ {
		if err := c.Select("B"); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(5200 * time.Millisecond)
	c.Close()
	if transport.calls.Load() != 0 {
		t.Fatal("idle runtime polled the account", transport.calls.Load())
	}
}

func TestConcurrentRefreshesShareOneImport(t *testing.T) {
	c, _, _ := setupClient(t)
	base := c.api.http.Transport
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	c.api.http.Transport = accountTransportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v3/client/profile" {
			calls.Add(1)
			once.Do(func() { close(entered) })
			<-release
		}
		return base.RoundTrip(r)
	})
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := c.Refresh(); err != nil {
				t.Error(err)
			}
		}()
	}
	awaitClosedSignal(t, entered, "profile not requested")
	time.Sleep(50 * time.Millisecond) // Let concurrently started callers reach the occupied sync mutex.
	close(release)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent refreshes became repeated HTTP requests", calls.Load())
	}
}
