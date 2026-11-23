package agent_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/agent"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/outbound"
	"github.com/KeepS13/HQIP/internal/reality"
	"github.com/KeepS13/HQIP/internal/testoutbound"
	"github.com/KeepS13/HQIP/runtime/nodepolicy"
	"github.com/apernet/hysteria/core/v2/billing"
	utls "github.com/metacubex/utls"
	"gopkg.in/yaml.v3"
)

func TestTrafficBillingRealDualProtocolHotUpdate(t *testing.T) {
	core, binary := os.Getenv("HQIP_E2E_CORE"), os.Getenv("HQIP_SINGBOX_EXE")
	if core == "" || binary == "" {
		t.Skip("set HQIP_E2E_CORE and HQIP_SINGBOX_EXE for isolated full-chain billing acceptance")
	}
	if !reality.VisionEnabled {
		t.Fatal("requires release tag with_utls")
	}
	f := newPortalE2E(t, core)
	user, subscription, passwords := f.account("billing", 64<<20, 3)
	f.wait(10*time.Second, func() bool {
		nodes, err := f.db.Nodes()
		if err != nil {
			return false
		}
		for _, node := range nodes {
			if !strings.Contains(fmt.Sprint(node.Capabilities), billing.Capability) {
				return false
			}
		}
		return true
	}, "node billing capability not confirmed")
	set := func(hy2, vless int64) int64 {
		t.Helper()
		previous, err := f.db.TrafficBillingSettings()
		if err != nil {
			t.Fatal(err)
		}
		next, err := f.db.SaveTrafficBillingSettings(model.TrafficBillingInput{Enabled: true, ExpectedRevision: previous.Revision, Hysteria2: hy2, VLESS: vless}, "wire-admin")
		if err != nil {
			t.Fatal(err)
		}
		return next.Revision
	}
	version1 := set(150, 150)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(180*4096))
		for range 180 {
			if _, err := w.Write(make([]byte, 4096)); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}))
	target.EnableHTTP2 = true
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	target.StartTLS()
	defer target.Close()
	first := f.client("sing-box", binary, 0, passwords[0], target.Certificate())
	defer first.stop()
	private, _ := ecdh.X25519().GenerateKey(rand.Reader)
	short := [8]byte{1, 3, 5, 7, 2, 4, 6, 8}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := model.RealityEndpoint{Port: listener.Addr().(*net.TCPAddr).Port, ServerName: "example.com", PublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), ShortID: hex.EncodeToString(short[:]), Flow: model.RealityVisionFlow}
	_, token, _, err := f.db.NodeSecrets(f.nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// Read only this isolated fixture's local business endpoint. The VLESS data
	// path must never dial its HY2 server, even while the HY2 client is active.
	var config struct {
		Admission nodepolicy.AdmissionConfig `yaml:"hqipAdmission"`
	}
	rawConfig, err := os.ReadFile(filepath.Join(f.root, f.nodes[0].ID, "server.yaml"))
	if err != nil || yaml.Unmarshal(rawConfig, &config) != nil {
		t.Fatal("fixture admission configuration", err)
	}
	config.Admission.Transport = "vless-direct"
	admission, err := nodepolicy.NewAdmission(config.Admission)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := reality.New(reality.Config{
		TLS: &utls.RealityConfig{Config: utls.Config{ServerName: "example.com", SessionTicketsDisabled: true}, Type: "tcp", Dest: target.Listener.Addr().String(), DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, ServerNames: map[string]bool{"example.com": true}, PrivateKey: private.Bytes(), ShortIds: map[[8]byte]bool{short: true}, MaxTimeDiff: 2 * time.Minute},
		Resolve: func(_ context.Context, id string) (string, error) {
			if id != model.RealityUUID(passwords[0]) {
				return "", fmt.Errorf("unknown alias")
			}
			return passwords[0], nil
		},
		Connect: func(ctx context.Context, password, clientIP string) (outbound.Client, error) {
			ticket, err := agent.RealityBillingPasswordForTest(f.nodes[0].ID, token, password, clientIP)
			if err != nil {
				return nil, err
			}
			return outbound.NewSession(ctx, admission, ticket, clientIP, testoutbound.Loopback{})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gateCtx, stopGate := context.WithCancel(ctx)
	gateDone := make(chan error, 1)
	go func() { gateDone <- gateway.Serve(gateCtx, listener) }()
	defer func() {
		stopGate()
		if err := <-gateDone; err != nil {
			t.Error(err)
		}
	}()
	outbound, err := (model.ClientNode{Protocol: "vless", Server: "127.0.0.1", Port: endpoint.Port, Password: model.RealityUUID(passwords[0]), SNI: endpoint.ServerName, Reality: &endpoint}).SingBoxOutbound("proxy")
	if err != nil {
		t.Fatal(err)
	}
	port := e2ePort(t, false)
	configuration := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}}, "outbounds": []any{outbound}, "route": map[string]any{"final": "proxy"}}
	raw, _ := json.Marshal(configuration)
	path := filepath.Join(t.TempDir(), "vless.json")
	os.WriteFile(path, raw, 0600)
	stopClient := startE2EProcess(t, binary, []string{"run", "-c", path})
	defer stopClient()
	f.wait(5*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, "Reality test client did not start")
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	second := &http.Client{Transport: transport, Timeout: 65 * time.Second}
	first.http.Timeout = 65 * time.Second
	third := f.client("sing-box", binary, 1, passwords[1], target.Certificate())
	defer third.stop()
	third.http.Timeout = 65 * time.Second
	results := make(chan error, 3)
	for _, client := range []*http.Client{first.http, second, third.http} {
		go func(client *http.Client) {
			response, err := client.Get(target.URL)
			if err == nil {
				var n int64
				n, err = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if err == nil && n != 180*4096 {
					err = fmt.Errorf("short transfer: %d", n)
				}
			}
			results <- err
		}(client)
	}
	type persisted struct {
		Grant    billing.Grant             `json:"grant"`
		Policies map[int64]billing.Policy  `json:"policies"`
		Segments map[int64]billing.Segment `json:"segments"`
	}
	states := func() []persisted {
		rows, err := f.db.DB.Query("SELECT b.state FROM traffic_lease_billing b JOIN portal_leases l ON l.id=b.lease_id WHERE l.subscription_id=?", subscription.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var values []persisted
		for rows.Next() {
			var raw string
			rows.Scan(&raw)
			var state persisted
			if err = json.Unmarshal([]byte(raw), &state); err != nil {
				t.Fatal(err)
			}
			values = append(values, state)
		}
		return values
	}
	confirmed := func(version int64) bool {
		values := states()
		if len(values) != 3 {
			return false
		}
		for _, state := range values {
			if state.Segments[version].AppliedAt.IsZero() {
				return false
			}
		}
		return true
	}
	f.wait(15*time.Second, func() bool { return confirmed(version1) }, "initial segments not confirmed")
	// All clients originate from loopback. HY2 instances remain separate from
	// the VLESS source slot, and all three compete against the same cap.
	current, err := f.db.UserSubscription(user.ID, subscription.ID)
	if err != nil || current.Occupied != 3 {
		t.Fatal("two HY2 instances plus one VLESS source did not occupy three slots", current, err)
	}
	f.rejectSourceIP(0, passwords[0], "127.0.0.1")
	leases, _ := f.db.PortalConnections(user.ID, "")
	ids := map[string]bool{}
	for _, l := range leases {
		ids[l.ID] = true
	}
	version2 := set(200, 75)
	f.wait(15*time.Second, func() bool { return confirmed(version2) }, "hot update did not reach both protocols")
	version3 := set(25, 300)
	f.wait(15*time.Second, func() bool { return confirmed(version3) }, "second hot update not confirmed")
	leases, _ = f.db.PortalConnections(user.ID, "")
	if len(leases) != 3 {
		t.Fatal("hot update reconnected a lease")
	}
	for _, l := range leases {
		if !ids[l.ID] || l.BandwidthBytesPerSecond != 33333 || l.Closed {
			t.Fatalf("hot update changed transport or real rate: %+v", l)
		}
	}
	for range 3 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal("continuous transfer interrupted", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	f.wait(10*time.Second, func() bool {
		var units int64
		for _, state := range states() {
			for version, segment := range state.Segments {
				units += (segment.Upload + segment.Download) * state.Policies[version].Multiplier
			}
		}
		sub, err := f.db.UserSubscription(user.ID, subscription.ID)
		return err == nil && sub.UsedBytes == units/100 && units > 0
	}, "weighted ledger did not match confirmed raw segments")
	t.Log("real two HY2 instances + one Reality source occupy three slots; full cap rejects another instance; two hot switches preserve leases, shared bandwidth and exact weighted settlement")
}
