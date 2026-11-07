package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/quic-go"
)

func nodeTestController(t *testing.T, count int) (*Controller, *fakeEngine) {
	t.Helper()
	c, e, _ := setupClient(t)
	c.mu.Lock()
	c.saved.SelectedSubscriptionID = c.saved.Profile.SubscriptionID
	c.saved.Profile.Nodes = nil
	for i := 0; i < count; i++ {
		c.saved.Profile.Nodes = append(c.saved.Profile.Nodes, Node{ID: fmt.Sprint(i), Ready: true, Password: "never-expose-credential"})
	}
	c.populateLocked(c.saved.Profile)
	c.mu.Unlock()
	return c, e
}
func waitNodeTests(t *testing.T, c *Controller) NodeTestView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := c.View().NodeTests
		if !v.Pending {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("node tests did not finish")
	return NodeTestView{}
}

func TestNodeDelaysBoundedAndNeverChangesConnection(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(fmt.Sprint(connected), func(t *testing.T) {
			c, e := nodeTestController(t, 8)
			e.alive = connected
			c.view.RunState = "failed"
			if connected {
				c.view.RunState = "connected"
				c.view.ConnectionIntent = true
				c.view.ActualNodeID = "0"
			}
			before := c.View().Status
			var active, peak, calls atomic.Int32
			c.nodeProbe = func(ctx context.Context, node Node) (int64, error) {
				calls.Add(1)
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				if node.ID == "4" {
					return 0, context.DeadlineExceeded
				}
				return 37, nil
			}
			if err := c.TestNodeDelays(); err != nil {
				t.Fatal(err)
			}
			if err := c.TestNodeDelays(); err != nil {
				t.Fatal(err)
			} // Duplicate click shares the run.
			v := waitNodeTests(t, c)
			if peak.Load() != 3 || calls.Load() != 8 {
				t.Fatalf("concurrency=%d probes=%d", peak.Load(), calls.Load())
			}
			if v.Results[4].Status != "timeout" || v.Results[0].DelayMS != 37 {
				t.Fatal(v)
			}
			if c.View().Status != before || e.Alive() != connected || len(e.calls) != 0 {
				t.Fatal("test changed active connection")
			}
			v.Results[0].Status = "mutated"
			if c.View().NodeTests.Results[0].Status != "success" {
				t.Fatal("view aliases controller results")
			}
			data, _ := json.Marshal(c.View())
			if strings.Contains(string(data), "never-expose-credential") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestNodeDelaysScopeCancellationAndClose(t *testing.T) {
	mutations := map[string]func(*Controller){
		"subscription":      func(c *Controller) { c.saved.SelectedSubscriptionID = "another" },
		"logout":            func(c *Controller) { c.api = nil; c.view.LoggedIn = false },
		"node-change":       func(c *Controller) { c.saved.Profile.Nodes[0].Port++ },
		"expired":           func(c *Controller) { c.saved.Profile.Subscription.ExpiresAt = time.Now().Add(-time.Second) },
		"mode-change":       func(c *Controller) { c.generation++ },
		"selection-pending": func(c *Controller) { c.view.SelectionPending = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			c, _ := nodeTestController(t, 4)
			started := make(chan struct{}, 4)
			cancelled := make(chan struct{}, 4)
			c.nodeProbe = func(ctx context.Context, _ Node) (int64, error) {
				started <- struct{}{}
				<-ctx.Done()
				cancelled <- struct{}{}
				return 999, nil
			}
			if err := c.TestNodeDelays(); err != nil {
				t.Fatal(err)
			}
			<-started
			c.mu.Lock()
			mutate(c)
			c.mu.Unlock()
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("scope change did not cancel")
			}
			v := waitNodeTests(t, c)
			if v.SubscriptionID != "" || len(v.Results) != 0 {
				t.Fatal("stale result published", v)
			}
		})
	}
	t.Run("explicit-cancel-and-retry", func(t *testing.T) {
		c, _ := nodeTestController(t, 1)
		started := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		c.nodeProbe = func(ctx context.Context, _ Node) (int64, error) {
			close(started)
			<-ctx.Done()
			<-release
			close(finished)
			return 999, nil
		}
		if err := c.TestNodeDelays(); err != nil {
			t.Fatal(err)
		}
		<-started
		c.CancelNodeDelays()
		if got := c.View().NodeTests; got.Pending || got.Results[0].Status != "cancelled" {
			t.Fatal(got)
		}
		c.nodeProbe = func(context.Context, Node) (int64, error) { return 25, nil }
		if err := c.TestNodeDelays(); err != nil {
			t.Fatal(err)
		}
		waitNodeTests(t, c)
		close(release)
		<-finished
		if got := c.View().NodeTests.Results[0]; got.Status != "success" || got.DelayMS != 25 {
			t.Fatal("old run overwrote retry", got)
		}
	})
	t.Run("close", func(t *testing.T) {
		c, _ := nodeTestController(t, 1)
		started := make(chan struct{})
		c.nodeProbe = func(ctx context.Context, _ Node) (int64, error) { close(started); <-ctx.Done(); return 0, ctx.Err() }
		if err := c.TestNodeDelays(); err != nil {
			t.Fatal(err)
		}
		<-started
		c.Close()
		if err := c.TestNodeDelays(); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestNodeDelaysRequireUsableSubscriptionAndReadyNode(t *testing.T) {
	c, _ := nodeTestController(t, 2)
	c.saved.Profile.Subscription.ExpiresAt = time.Now().Add(-time.Second)
	if err := c.TestNodeDelays(); err == nil {
		t.Fatal("allowed expired subscription")
	}
	c.saved.Profile.Subscription.ExpiresAt = time.Now().Add(time.Hour)
	c.saved.Profile.Nodes[0].Ready = false
	var calls atomic.Int32
	c.nodeProbe = func(context.Context, Node) (int64, error) { calls.Add(1); return 10, nil }
	if err := c.TestNodeDelays(); err != nil {
		t.Fatal(err)
	}
	v := waitNodeTests(t, c)
	if calls.Load() != 1 || v.Results[0].Status != "unavailable" {
		t.Fatal(v)
	}
}

func TestQUICProbeVerifiesCertificatesAndSendsNoApplicationStream(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"node.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	server, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"h3"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var streams atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := server.Accept(ctx)
			if err != nil {
				return
			}
			if _, err = connection.AcceptStream(ctx); err == nil {
				streams.Add(1)
			}
		}
	}()
	for _, test := range []struct {
		name, sni string
		roots     *x509.CertPool
		ok        bool
	}{{"valid", "node.test", roots, true}, {"wrong-sni", "other.test", roots, false}, {"untrusted", "node.test", x509.NewCertPool(), false}} {
		t.Run(test.name, func(t *testing.T) {
			packet, err := directProbePacket(ctx, server.Addr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer packet.Close()
			delay, err := measureQUICHandshake(ctx, packet, server.Addr(), &tls.Config{ServerName: test.sni, RootCAs: test.roots, NextProtos: []string{"h3"}})
			if test.ok {
				if err != nil || delay < 1 {
					t.Fatalf("delay=%d err=%v", delay, err)
				}
			} else if err == nil || nodeProbeStatus(err) != "certificate_error" {
				t.Fatalf("certificate not rejected/classified: %v (%s)", err, nodeProbeStatus(err))
			}
		})
	}
	cancel()
	server.Close()
	<-done
	if streams.Load() != 0 {
		t.Fatal("probe sent an application stream")
	}
}

func TestQUICProbeDeadlineAndDirectFailure(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	packet, err := directProbePacket(ctx, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	_, err = measureQUICHandshake(ctx, packet, server.LocalAddr(), &tls.Config{ServerName: "node.test", NextProtos: []string{"h3"}})
	if nodeProbeStatus(err) != "timeout" {
		t.Fatalf("timeout: %v", err)
	}
	if nodeProbeStatus(errProbeDirect) != "direct_unavailable" {
		t.Fatal("direct error not identified")
	}
}
