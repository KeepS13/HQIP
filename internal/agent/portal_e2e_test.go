package agent_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/agent"
	"github.com/KeepS13/HQIP/internal/control"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
)

// Explicit opt-in: launches only private loopback processes and temporary data.
// No production configuration, system proxy, routes, DNS or credentials are used.
func TestPortalFullChainTwoNodesRealClients(t *testing.T) {
	core := os.Getenv("HQIP_E2E_CORE")
	singbox := os.Getenv("HQIP_SINGBOX")
	mihomo := os.Getenv("HQIP_MIHOMO")
	if core == "" || singbox == "" || mihomo == "" {
		t.Skip("HQIP_E2E_CORE, HQIP_SINGBOX and HQIP_MIHOMO required")
	}
	f := newPortalE2E(t, core)
	u, subscription, passwords := f.account("shared", 8<<20, 2)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		if count <= 0 {
			count = 64
		}
		w.Header().Set("Content-Length", strconv.Itoa(count))
		_, _ = io.CopyN(w, zeroE2EReader{}, int64(count))
	}))
	defer target.Close()
	first := f.client("sing-box", singbox, 0, passwords[0], target.Certificate())
	defer first.stop()
	f.fetch(first, target.URL, 64)
	leases, err := f.db.PortalConnections(u.ID, "")
	if err != nil || len(leases) != 1 {
		t.Fatal("first real lease missing", err)
	}
	if leases[0].BandwidthBytesPerSecond != 100000 || leases[0].RatePending {
		t.Fatalf("single connection did not obtain full per-direction rate: %+v", leases[0])
	}
	second := f.client("mihomo", mihomo, 1, passwords[1], target.Certificate())
	defer second.stop()
	secondResult := make(chan error, 1)
	go func() {
		response, e := second.http.Get(target.URL)
		if e == nil {
			_, e = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		secondResult <- e
	}()
	f.wait(5*time.Second, func() bool {
		var n int
		_ = f.db.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE user_id=? AND closed=0", u.ID).Scan(&n)
		return n == 2
	}, "two nodes did not create independent real leases")
	var pending int
	_ = f.db.DB.QueryRow("SELECT SUM(pending) FROM portal_rates WHERE lease_id IN (SELECT id FROM portal_leases WHERE user_id=?)", u.ID).Scan(&pending)
	if pending == 0 {
		t.Fatal("second node was not paused before first-node rate decrease acknowledgment")
	}
	select {
	case e := <-secondResult:
		t.Fatal("new share forwarded before handshake completed", e)
	case <-time.After(200 * time.Millisecond):
	}
	f.wait(20*time.Second, func() bool {
		var n int
		_ = f.db.DB.QueryRow("SELECT COUNT(*) FROM portal_leases l JOIN portal_rates r ON r.lease_id=l.id WHERE l.user_id=? AND l.closed=0 AND l.bandwidth=50000 AND r.pending=0 AND r.ack=r.version", u.ID).Scan(&n)
		return n == 2
	}, "both nodes did not acknowledge equal dynamic rates")
	select {
	case e := <-secondResult:
		if e != nil {
			t.Fatal("second authorized node transfer failed", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second transfer remained blocked")
	}
	t.Log("real controller + Agents + two cores: 1->2 connection shares and applied-rate ACK passed")
	f.rejectSourceIP(1, passwords[1], "127.0.0.2")
	e, err := f.db.UserSubscription(u.ID, subscription.ID)
	if err != nil || e.Occupied != 2 {
		t.Fatal("cross-node occupied count incorrect", err)
	}
	f.fetch(first, target.URL, 64)
	f.fetch(second, target.URL, 64)
	second.stop()
	// Forced process termination sends no QUIC close; the server must retain the
	// slot until QUIC idle detection confirms closure, rather than reuse it early.
	f.wait(45*time.Second, func() bool {
		var count int
		_ = f.db.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=? AND closed=0 AND expires>?", subscription.ID, time.Now().Unix()).Scan(&count)
		return count == 1
	}, "crashed second real connection did not release after QUIC idle closure")
	// The remaining instance keeps its own slot after the other one closes.
	if current, err := f.db.UserSubscription(u.ID, subscription.ID); err != nil || current.Occupied != 1 {
		t.Fatal("same-IP disconnect disturbed the remaining connection", current, err)
	}
	f.wait(12*time.Second, func() bool {
		leases, _ := f.db.PortalConnections(u.ID, "")
		for _, l := range leases {
			if !l.Closed && l.ExpiresAt.After(time.Now()) {
				return l.BandwidthBytesPerSecond == 100000 && !l.RatePending
			}
		}
		return false
	}, "remaining connection did not recover full rate")
	f.fetch(first, target.URL, 64)
	var expiry int64
	var expiredLeaseID string
	if err = f.db.DB.QueryRow("SELECT id,expires FROM portal_leases WHERE user_id=? AND closed=0 ORDER BY expires DESC LIMIT 1", u.ID).Scan(&expiredLeaseID, &expiry); err != nil {
		t.Fatal(err)
	}
	deadline := time.Unix(expiry, 0)
	cut := time.Now()
	f.offline.Store(true)
	first.http.Timeout = 3 * time.Second
	if deadline.After(cut.Add(120 * time.Second)) {
		t.Fatal("grant exceeded 120 seconds")
	}
	f.fetch(first, target.URL, 64)
	var ended time.Time
	for time.Now().Before(deadline.Add(4 * time.Second)) {
		time.Sleep(2 * time.Second)
		response, e := first.http.Get(target.URL)
		if e != nil {
			ended = time.Now()
			break
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if time.Since(cut) > 30*time.Second && int(time.Since(cut).Seconds())%30 < 2 {
			t.Logf("control transport disconnected for %ds; existing reserved connection remains bounded by original deadline", int(time.Since(cut).Seconds()))
		}
	}
	if ended.IsZero() || ended.Before(deadline.Add(-2*time.Second)) || ended.After(deadline.Add(5*time.Second)) {
		t.Fatalf("lease did not end at original deadline: expires=%s ended=%s", deadline, ended)
	}
	var unchanged int64
	_ = f.db.DB.QueryRow("SELECT expires FROM portal_leases WHERE user_id=? AND closed=0 ORDER BY expires DESC LIMIT 1", u.ID).Scan(&unchanged)
	if unchanged != expiry {
		t.Fatal("disconnected controller somehow extended authorization")
	}
	oldEntitlement, _ := f.db.UserSubscription(u.ID, subscription.ID)
	if oldEntitlement.ReservedBytes == 0 {
		t.Fatal("unconfirmed final release incorrectly reused reserved quota")
	}
	f.offline.Store(false)
	f.wait(10*time.Second, func() bool {
		response, e := first.http.Get(target.URL)
		if e != nil {
			return false
		}
		response.Body.Close()
		return true
	}, "client could not acquire a fresh connection after control recovered")
	leases, _ = f.db.PortalConnections(u.ID, "")
	if len(leases) < 3 {
		t.Fatal("core reused expired lease after recovery")
	}
	f.wait(12*time.Second, func() bool {
		var closed int
		_ = f.db.DB.QueryRow("SELECT closed FROM portal_leases WHERE id=?", expiredLeaseID).Scan(&closed)
		return closed == 1
	}, "Agent did not replay durable final usage after control recovered")
	settled, _ := f.db.UserSubscription(u.ID, subscription.ID)
	if settled.ReservedBytes >= oldEntitlement.ReservedBytes+(1<<20) {
		t.Fatal("confirmed old final did not return its unused reservation")
	}
	t.Logf("control transport outage lasted %.1fs; original lease expired and reconnect acquired fresh identity", time.Since(cut).Seconds())
	first.stop()
	// A separate entitlement drives both real nodes to quota exhaustion. No
	// resets/time travel are used, and payload allowance is measured centrally.
	quotaUser, quotaSubscription, quotaPasswords := f.account("quota", 1536<<10, 2)
	qa := f.client("sing-box", singbox, 0, quotaPasswords[0], target.Certificate())
	defer qa.stop()
	qb := f.client("mihomo", mihomo, 1, quotaPasswords[1], target.Certificate())
	defer qb.stop()
	qa.http.Timeout = 50 * time.Second
	qb.http.Timeout = 50 * time.Second
	results := make(chan int64, 2)
	for _, c := range []*portalE2EClient{qa, qb} {
		go func(c *portalE2EClient) {
			response, e := c.http.Get(target.URL + "?bytes=4194304")
			var n int64
			if e == nil {
				n, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
			results <- n
		}(c)
	}
	bodyBytes := <-results
	bodyBytes += <-results
	qa.stop()
	qb.stop()
	f.wait(45*time.Second, func() bool {
		e, _ := f.db.UserSubscription(quotaUser.ID, quotaSubscription.ID)
		return e != nil && e.Occupied == 0
	}, "quota-exhausted sessions remained active")
	qe, err := f.db.UserSubscription(quotaUser.ID, quotaSubscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	if qe.UsedBytes <= 0 || qe.UsedBytes+qe.ReservedBytes > 1536<<10 || bodyBytes >= 1536<<10 {
		t.Fatalf("shared quota violated: used=%d reserved=%d delivered=%d", qe.UsedBytes, qe.ReservedBytes, bodyBytes)
	}
	t.Logf("two-node quota: used=%d reserved=%d, plaintext delivered=%d; cap=%d", qe.UsedBytes, qe.ReservedBytes, bodyBytes, 1536<<10)
}

type zeroE2EReader struct{}

func TestPortalSameTemplateCopiesRealClients(t *testing.T) {
	core, singbox, mihomo := os.Getenv("HQIP_E2E_CORE"), os.Getenv("HQIP_SINGBOX"), os.Getenv("HQIP_MIHOMO")
	if core == "" || singbox == "" || mihomo == "" {
		t.Skip("HQIP_E2E_CORE, HQIP_SINGBOX and HQIP_MIHOMO required")
	}
	f := newPortalE2E(t, core)
	u, firstSubscription, firstPasswords := f.account("same-template", 8<<20, 1)
	u, err := f.db.PortalUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	template, err := f.db.SubscriptionTemplate(firstSubscription.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	secondSubscription, err := f.db.AssignSubscription(u.ID, template.ID, "second-copy", u.Revision, template.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.PortalSubscription(u.ID, secondSubscription.ID, "create"); err != nil {
		t.Fatal(err)
	}
	secondPassword, err := f.db.PortalCredential(u.ID, secondSubscription.ID, f.nodes[1].ID, "subscription")
	if err != nil {
		t.Fatal(err)
	}
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		if count <= 0 {
			count = 64
		}
		w.Header().Set("Content-Length", strconv.Itoa(count))
		_, _ = io.CopyN(w, zeroE2EReader{}, int64(count))
	}))
	defer target.Close()
	a := f.client("sing-box", singbox, 0, firstPasswords[0], target.Certificate())
	defer a.stop()
	b := f.client("mihomo", mihomo, 1, secondPassword, target.Certificate())
	defer b.stop()
	f.fetch(a, target.URL+"?bytes=65536", 65536)
	f.fetch(b, target.URL+"?bytes=131072", 131072)
	f.wait(12*time.Second, func() bool {
		first, _ := f.db.UserSubscription(u.ID, firstSubscription.ID)
		second, _ := f.db.UserSubscription(u.ID, secondSubscription.ID)
		return first != nil && second != nil && first.UsedBytes > 65536 && second.UsedBytes > 131072
	}, "real usage was not charged into both independent subscriptions")
	leases, err := f.db.PortalConnections(u.ID, "")
	if err != nil || len(leases) != 2 {
		t.Fatal("independent copies did not each receive a real connection", err)
	}
	if leases[0].AccountingID == leases[1].AccountingID || leases[0].SubscriptionID == leases[1].SubscriptionID {
		t.Fatal("identical template purchases reused accounting identity")
	}
	for _, l := range leases {
		if l.BandwidthBytesPerSecond != 100000 || l.RatePending {
			t.Fatalf("different subscription took part of this copy's bandwidth: %+v", l)
		}
	}
	f.rejectSourceIP(1, firstPasswords[1], "127.0.0.2")
	firstCurrent, err := f.db.UserSubscription(u.ID, firstSubscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.RevokeSubscription(u.ID, firstSubscription.ID, firstCurrent.Revision); err != nil {
		t.Fatal(err)
	}
	f.wait(12*time.Second, func() bool {
		own, _ := f.db.PortalConnections(u.ID, "", firstSubscription.ID)
		return len(own) == 1 && own[0].Closed
	}, "removing first copy did not precisely close its real connection")
	f.fetch(b, target.URL+"?bytes=32768", 32768)
	active, err := f.db.UserSubscription(u.ID, secondSubscription.ID)
	if err != nil || active.Status != "active" || active.Occupied != 1 {
		t.Fatal("removing first copy disturbed second copy", err)
	}
	if err = f.db.RevokeSubscription(u.ID, secondSubscription.ID, active.Revision); err != nil {
		t.Fatal(err)
	}
	f.wait(12*time.Second, func() bool {
		own, _ := f.db.PortalConnections(u.ID, "", secondSubscription.ID)
		return len(own) == 1 && own[0].Closed
	}, "second copy final usage was not settled")
	for _, sub := range []model.UserSubscription{firstSubscription, secondSubscription} {
		settled, err := f.db.UserSubscription(u.ID, sub.ID)
		if err != nil || settled.ReservedBytes != 0 {
			t.Fatal("closed copy retained an unconfirmed budget", err)
		}
		own, err := f.db.PortalConnections(u.ID, "", sub.ID)
		if err != nil || len(own) != 1 || settled.UsedBytes != own[0].UploadBytes+own[0].DownloadBytes {
			t.Fatal("subscription usage did not match its own final core counters", err)
		}
		t.Logf("copy %s: confirmed=%d reserved=%d; precise final receipt matches own core", sub.ID, settled.UsedBytes, settled.ReservedBytes)
	}
}

func (zeroE2EReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type portalE2E struct {
	t                          *testing.T
	db                         *store.Store
	offline                    atomic.Bool
	nodes                      []model.Node
	stopCores                  []func()
	certPath, keyPath, certPEM string
	root                       string
}

func newPortalE2E(t *testing.T, core string) *portalE2E {
	t.Helper()
	root := t.TempDir()
	f := &portalE2E{t: t, root: root}
	db, err := store.Open(filepath.Join(root, "controller"), "http://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
	t.Cleanup(func() { _ = db.Close() })
	handler := control.New(db, control.Options{PublicURL: "http://127.0.0.1", AdminOrigin: "http://127.0.0.1"}).PublicHandler()
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.offline.Load() {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				conn.Close()
			}
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(central.Close)
	db.BaseURL = central.URL
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "full-chain.invalid"}, DNSNames: []string{"full-chain.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	f.certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	f.certPath = filepath.Join(root, "ca.pem")
	f.keyPath = filepath.Join(root, "key.pem")
	_ = os.WriteFile(f.certPath, []byte(f.certPEM), 0600)
	_ = os.WriteFile(f.keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600)
	for i := 0; i < 2; i++ {
		port := e2ePort(t, true)
		n, err := db.CreateNode(model.Node{Name: fmt.Sprintf("isolated-%d", i), SSHHost: "127.0.0.1", SSHPort: 22, SSHUser: "root", PublicIP: "127.0.0.1", Domain: fmt.Sprintf("isolated-%d.invalid", i), Port: port}, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.DB.Exec("UPDATE nodes SET config=json_set(config,'$.domain','full-chain.invalid','$.status','ready'),capabilities='[\"admission-v1\"]' WHERE id=?", n.ID)
		if err != nil {
			t.Fatal(err)
		}
		n.Domain = "full-chain.invalid"
		n.Status = "ready"
		f.nodes = append(f.nodes, n)
		_, token, _, err := db.NodeSecrets(n.ID)
		if err != nil {
			t.Fatal(err)
		}
		agentPort, statsPort := e2ePort(t, false), e2ePort(t, false)
		secret := secure.Token()
		nodeDir := filepath.Join(root, n.ID)
		_ = os.MkdirAll(nodeDir, 0700)
		a, err := agent.NewPortalE2EAgentForTest(t, model.AgentConfig{ControlURL: central.URL, NodeID: n.ID, Token: token, Listen: fmt.Sprintf("127.0.0.1:%d", agentPort), StatePath: filepath.Join(nodeDir, "ledger.db"), StatsURL: fmt.Sprintf("http://127.0.0.1:%d", statsPort), StatsSecret: secret})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done; _ = a.Close() })
		yaml := fmt.Sprintf("listen: 127.0.0.1:%d\ntls:\n  cert: %q\n  key: %q\n  sniGuard: strict\nauth:\n  type: http\n  http:\n    url: http://127.0.0.1:%d/auth?secret=%s\nhqipAdmission:\n  url: http://127.0.0.1:%d\n  secret: %s\ntrafficStats:\n  listen: 127.0.0.1:%d\n  secret: %s\n", port, filepath.ToSlash(f.certPath), filepath.ToSlash(f.keyPath), agentPort, secret, agentPort, secret, statsPort, secret)
		cfg := filepath.Join(nodeDir, "server.yaml")
		_ = os.WriteFile(cfg, []byte(yaml), 0600)
		f.stopCores = append(f.stopCores, startE2EProcess(t, core, []string{"server", "-c", cfg}))
		f.wait(8*time.Second, func() bool {
			var caps struct{ Admission bool }
			r, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/hqip/capabilities", statsPort), nil)
			r.Header.Set("Authorization", secret)
			client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
			response, e := client.Do(r)
			if e != nil {
				return false
			}
			defer response.Body.Close()
			defer client.CloseIdleConnections()
			return response.StatusCode == 200 && json.NewDecoder(response.Body).Decode(&caps) == nil && caps.Admission
		}, "new core did not advertise real admission capability")
	}
	_, _ = db.DB.Exec("UPDATE jobs SET state='done'")
	if err = db.ActivatePortal(); err != nil {
		t.Fatal(err)
	}
	f.wait(12*time.Second, func() bool {
		for _, n := range f.nodes {
			var healthy int
			var wanted, applied int64
			if db.DB.QueryRow("SELECT core_healthy,desired_revision,applied_revision FROM nodes WHERE id=?", n.ID).Scan(&healthy, &wanted, &applied) != nil || healthy != 1 || applied < wanted {
				return false
			}
		}
		return true
	}, "real Agents did not synchronize activation and sampled health")
	return f
}

func (f *portalE2E) account(label string, quota int64, slots int) (model.PortalUser, model.UserSubscription, []string) {
	f.t.Helper()
	u, e := f.db.RegisterPortal(label+"@example.invalid", secure.Token())
	if e != nil {
		f.t.Fatal(e)
	}
	rate := 0.8
	ids := []string{f.nodes[0].ID, f.nodes[1].ID}
	p, e := f.db.SaveSubscriptionTemplate("", model.SubscriptionTemplate{Name: label, Enabled: true, Listed: true, ValidityDays: 1, NodeIDs: ids, DefaultNodeID: ids[0], BandwidthMbps: &rate, QuotaBytes: &quota, MaxConnections: slots}, 0)
	if e != nil {
		f.t.Fatal(e)
	}
	subscription, e := f.db.AssignSubscription(u.ID, p.ID, "initial-subscription", u.Revision, p.Revision)
	if e != nil {
		f.t.Fatal(e)
	}
	if _, e = f.db.PortalSubscription(u.ID, subscription.ID, "create"); e != nil {
		f.t.Fatal(e)
	}
	passwords := []string{}
	for _, id := range ids {
		password, e := f.db.PortalCredential(u.ID, subscription.ID, id, "subscription")
		if e != nil {
			f.t.Fatal(e)
		}
		passwords = append(passwords, password)
	}
	current, e := f.db.UserSubscription(u.ID, subscription.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	subscription = *current
	return u, subscription, passwords
}

type portalE2EClient struct {
	http *http.Client
	stop func()
}

func (f *portalE2E) client(kind, binary string, node int, password string, targetCert *x509.Certificate) *portalE2EClient {
	t := f.t
	t.Helper()
	dir := t.TempDir()
	port := e2ePort(t, true)
	controller := ""
	cfg := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}}, "outbounds": []any{map[string]any{"type": "hysteria2", "tag": "node", "server": "127.0.0.1", "server_port": f.nodes[node].Port, "password": password, "tls": map[string]any{"enabled": true, "server_name": "full-chain.invalid", "certificate_path": f.certPath}}}, "route": map[string]any{"final": "node"}}
	if kind == "mihomo" {
		controller = fmt.Sprintf("127.0.0.1:%d", e2ePort(t, false))
		cfg = map[string]any{"mixed-port": port, "bind-address": "127.0.0.1", "allow-lan": false, "mode": "rule", "log-level": "error", "external-controller": controller, "secret": "isolated-test-controller-secret", "dns": map[string]any{"enable": false}, "tls": map[string]any{"custom-certifactes": []string{f.certPEM}}, "proxies": []any{map[string]any{"name": "node", "type": "hysteria2", "server": "127.0.0.1", "port": f.nodes[node].Port, "password": password, "sni": "full-chain.invalid", "skip-cert-verify": false}}, "rules": []string{"MATCH,node"}}
	}
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	_ = os.WriteFile(path, raw, 0600)
	args := []string{"run", "-c", path}
	if kind == "mihomo" {
		args = []string{"-d", dir, "-f", path}
	}
	stop := startE2EProcess(t, binary, args)
	f.wait(6*time.Second, func() bool {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	}, "isolated client did not listen")
	if kind == "mihomo" {
		body, _ := json.Marshal(map[string]string{"path": path})
		r, _ := http.NewRequest(http.MethodPut, "http://"+controller+"/configs", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer isolated-test-controller-secret")
		r.Header.Set("Content-Type", "application/json")
		c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		resp, e := c.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		c.CloseIdleConnections()
		if resp.StatusCode != 204 {
			t.Fatal("test CA reparse failed")
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(targetCert)
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	tr := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &portalE2EClient{http: &http.Client{Transport: tr, Timeout: 25 * time.Second}, stop: stop}
}

func (f *portalE2E) fetch(c *portalE2EClient, address string, want int) {
	f.t.Helper()
	r, e := c.http.Get(address)
	if e != nil {
		f.t.Fatal("real HTTPS proxy failed", e)
	}
	n, e := io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if e != nil || n != int64(want) {
		f.t.Fatal("real HTTPS payload mismatch", n, e)
	}
}
func (f *portalE2E) wait(timeout time.Duration, check func() bool, message string) {
	f.t.Helper()
	for end := time.Now().Add(timeout); time.Now().Before(end); {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatal(message)
}
func e2ePort(t *testing.T, udp bool) int {
	t.Helper()
	for i := 0; i < 100; i++ {
		if udp {
			// Windows may offer a TCP ephemeral port reserved for UDP by
			// Hyper-V. Choose UDP first, then verify the same TCP port.
			packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := packet.LocalAddr().(*net.UDPAddr).Port
			listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
			packet.Close()
			if err == nil {
				listener.Close()
				return port
			}
			continue
		}
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		port := l.Addr().(*net.TCPAddr).Port
		if !udp {
			l.Close()
			return port
		}
		p, e := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		l.Close()
		if e == nil {
			p.Close()
			return port
		}
	}
	t.Fatal("no isolated test port")
	return 0
}
func startE2EProcess(t *testing.T, binary string, args []string) func() {
	t.Helper()
	var logs bytes.Buffer
	p := exec.Command(binary, args...)
	p.Stdout = &logs
	p.Stderr = &logs
	if e := p.Start(); e != nil {
		t.Fatal(e)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = p.Process.Kill()
			_ = p.Wait()
			if t.Failed() {
				t.Log(filepath.Base(binary), logs.String())
			}
		})
	}
	t.Cleanup(stop)
	return stop
}
