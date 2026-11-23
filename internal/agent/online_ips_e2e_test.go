package agent_test

import (
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	hy "github.com/apernet/hysteria/core/v2/client"
	hyErrors "github.com/apernet/hysteria/core/v2/errors"
)

type sourceIPFactory string

func (f sourceIPFactory) New(net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(string(f))})
}

// A full subscription can authenticate an idle QUIC transport. Only a rejected
// target dial proves admission enforced the limit; TLS/network errors do not.
func (f *portalE2E) rejectSourceIP(node int, password, ip string) {
	f.t.Helper()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(f.certPEM))
	other, _, err := hy.NewClient(&hy.Config{ConnFactory: sourceIPFactory(ip), ServerAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: f.nodes[node].Port}, Auth: password, TLSConfig: hy.TLSConfig{ServerName: "full-chain.invalid", RootCAs: roots}, QUICConfig: hy.QUICConfig{DisableChromeParrot: true}})
	if err != nil {
		f.t.Fatal("idle transport failed before target admission", err)
	}
	defer other.Close()
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	defer target.Close()
	conn, err := other.TCP(target.Addr().String())
	if err == nil {
		conn.Close()
		f.t.Fatal("another instance forwarded through a full subscription")
	}
	var denied hyErrors.DialError
	if !errors.As(err, &denied) || !strings.Contains(denied.Message, "HQIP admission unavailable or denied") {
		f.t.Fatal("different-IP target failed for a reason other than admission", err)
	}
}

// Three real third-party HY2 processes share an IP on the same and other nodes. Each
// uses a slot; another real instance is refused even with the same source IP.
func TestOnlineIPRealTwoNodesAndDifferentSource(t *testing.T) {
	core, binary := os.Getenv("HQIP_E2E_CORE"), os.Getenv("HQIP_SINGBOX_EXE")
	if core == "" || binary == "" {
		t.Skip("isolated core and sing-box paths required")
	}
	f := newPortalE2E(t, core)
	for _, n := range f.nodes {
		if err := f.db.SetOfflineAdmissionEnabled(n.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	f.wait(15*time.Second, func() bool {
		for _, n := range f.nodes {
			var wanted, applied int64
			if err := f.db.DB.QueryRow("SELECT desired_revision,applied_revision FROM nodes WHERE id=?", n.ID).Scan(&wanted, &applied); err != nil || wanted > applied {
				return false
			}
		}
		return true
	}, "node quota not applied")
	// Leave room for both same-node clients' existing 16 MiB byte grants so
	// a quota reservation cannot masquerade as rejection by the online cap.
	user, subscription, passwords := f.account("online-instance", 64<<20, 3)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.CopyN(w, zeroE2EReader{}, 4096) }))
	defer target.Close()
	first := f.client("sing-box", binary, 0, passwords[0], target.Certificate())
	defer first.stop()
	f.fetch(first, target.URL, 4096)
	second := f.client("sing-box", binary, 1, passwords[1], target.Certificate())
	defer second.stop()
	f.fetch(second, target.URL, 4096)
	third := f.client("sing-box", binary, 0, passwords[0], target.Certificate())
	defer third.stop()
	f.fetch(third, target.URL, 4096)
	e, err := f.db.UserSubscription(user.ID, subscription.ID)
	if err != nil || e.Occupied != 3 {
		t.Fatal("same source merged separate HY2 instances", e, err)
	}
	var peers, parents int
	if err = f.db.DB.QueryRow("SELECT COUNT(DISTINCT client_ip),COUNT(*) FROM portal_leases WHERE subscription_id=? AND client_ip='127.0.0.1'", subscription.ID).Scan(&peers, &parents); err != nil || peers != 1 || parents != 3 {
		t.Fatal("socket source missing from ledger", peers, parents, err)
	}
	f.rejectSourceIP(0, passwords[0], "127.0.0.1")
	f.rejectSourceIP(0, passwords[0], "127.0.0.2")
	// Existing third-party connections continue after the rejected new source.
	f.fetch(first, target.URL, 4096)
	f.fetch(second, target.URL, 4096)
	f.fetch(third, target.URL, 4096)
	first.stop()
	f.wait(45*time.Second, func() bool {
		var count int
		_ = f.db.DB.QueryRow("SELECT COUNT(*) FROM portal_leases WHERE subscription_id=? AND expires>? AND closed=0", subscription.ID, time.Now().Unix()).Scan(&count)
		return count == 2
	}, "first node did not release its ended connection")
	e, err = f.db.UserSubscription(user.ID, subscription.ID)
	if err != nil || e.Occupied != 2 {
		t.Fatal("one disconnect released another live same-IP connection", e, err)
	}
	second.stop()
	f.wait(45*time.Second, func() bool {
		e, _ := f.db.UserSubscription(user.ID, subscription.ID)
		return e != nil && e.Occupied == 1
	}, "second disconnect released the other node's instance")
	f.fetch(third, target.URL, 4096)
	third.stop()
	f.wait(45*time.Second, func() bool {
		e, _ := f.db.UserSubscription(user.ID, subscription.ID)
		return e != nil && e.Occupied == 0 && e.UsedBytes > 0
	}, "last disconnect did not release slot or settle usage")
}
