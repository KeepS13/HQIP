package agent_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/agent"
	"github.com/KeepS13/HQIP/internal/outbound"
	"github.com/KeepS13/HQIP/internal/testoutbound"
	"github.com/KeepS13/HQIP/runtime/nodepolicy"
	"gopkg.in/yaml.v3"
)

// Complements the real Reality/Vision wire test with process failure plus the
// real controller, local quota ledger, slot allocator and revocation path.
func TestRealityDirectSurvivesHY2ProcessCrashAndRestart(t *testing.T) {
	core := os.Getenv("HQIP_E2E_CORE")
	if core == "" {
		t.Skip("HQIP_E2E_CORE required for isolated process failure acceptance")
	}
	f := newPortalE2E(t, core)
	user, subscription, passwords := f.account("direct-independent", 64<<20, 2)
	for _, node := range f.nodes {
		if err := f.db.SetOfflineAdmissionEnabled(node.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	f.wait(12*time.Second, func() bool {
		var desired, applied int64
		_ = f.db.DB.QueryRow("SELECT desired_revision,applied_revision FROM nodes WHERE id=?", f.nodes[0].ID).Scan(&desired, &applied)
		return applied >= desired
	}, "offline configuration not synchronized")
	path := filepath.Join(f.root, f.nodes[0].ID, "server.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Admission nodepolicy.AdmissionConfig `yaml:"hqipAdmission"`
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Admission.Transport = "vless-direct"
	_, token, _, err := f.db.NodeSecrets(f.nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	connect := func(ip string) outbound.Client {
		t.Helper()
		ticket, e := agent.RealityBillingPasswordForTest(f.nodes[0].ID, token, passwords[0], ip)
		if e != nil {
			t.Fatal(e)
		}
		admission, e := nodepolicy.NewAdmission(cfg.Admission)
		if e != nil {
			t.Fatal(e)
		}
		client, e := outbound.NewSession(context.Background(), admission, ticket, ip, testoutbound.Loopback{})
		if e != nil {
			t.Fatal("direct admission depended on HY2", e)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	first := connect("198.51.100.1")
	socket, err := first.TCP(echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	exchange := func(c net.Conn) {
		t.Helper()
		// Adding a source waits for the existing shared-rate decrease/ACK round.
		_ = c.SetDeadline(time.Now().Add(20 * time.Second))
		payload := []byte("independent VLESS payload")
		if _, e := c.Write(payload); e != nil {
			t.Fatal("VLESS interrupted", e)
		}
		got := make([]byte, len(payload))
		if _, e := io.ReadFull(c, got); e != nil || string(got) != string(payload) {
			t.Fatal("VLESS data mismatch", e)
		}
	}
	exchange(socket)
	id := first.Snapshot().ID
	f.stopCores[0]() // Kill only the test-owned process; no graceful QUIC final.
	for range 7 {
		exchange(socket)
		time.Sleep(time.Second)
	}
	second := connect("198.51.100.2") // New authorization while HY2 remains stopped.
	other, err := second.TCP(echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	exchange(other)
	_ = other.Close()
	_ = second.Close()
	stop := startE2EProcess(t, core, []string{"server", "-c", path})
	defer stop()
	for range 6 {
		exchange(socket)
		time.Sleep(time.Second)
	}
	if first.Snapshot().ID != id || first.Snapshot().Closed {
		t.Fatal("HY2 restart replaced live VLESS lease")
	}
	f.wait(12*time.Second, func() bool {
		e, err := f.db.UserSubscription(user.ID, subscription.ID)
		return err == nil && e.Occupied == 1 && e.UsedBytes > 0
	}, "shared slot release or settlement failed after HY2 restart")
	if _, err = f.db.PortalSubscription(user.ID, subscription.ID, "rotate"); err != nil {
		t.Fatal(err)
	}
	_ = socket.SetReadDeadline(time.Now().Add(12 * time.Second))
	buffer := make([]byte, 1)
	if _, err = socket.Read(buffer); err == nil {
		t.Fatal("revoked direct socket remained open")
	}
	f.wait(12*time.Second, func() bool { return first.Snapshot().Closed }, "revocation did not close direct lease")
	t.Log("same VLESS lease and target socket survived a real HY2 crash/restart; new admission, renewal, release, billing and rotation also passed")
}
