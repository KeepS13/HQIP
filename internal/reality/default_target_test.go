package reality_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/outbound"
	"github.com/KeepS13/HQIP/internal/reality"
	utls "github.com/metacubex/utls"
)

// This opt-in deployment check uses the real default target over the Internet.
// It authenticates a local Reality ingress without opening a user proxy lease;
// ordinary CI stays deterministic and uses the local-target wire regression.
func TestDefaultRealityTargetHandshake(t *testing.T) {
	if os.Getenv("HQIP_REALITY_TARGET_SMOKE") != "1" {
		t.Skip("set HQIP_REALITY_TARGET_SMOKE=1 to verify the external default target")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var short [8]byte
	if _, err = rand.Read(short[:]); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	endpoint := model.RealityEndpoint{Port: listener.Addr().(*net.TCPAddr).Port, ServerName: model.DefaultRealityServerName,
		PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), ShortID: hex.EncodeToString(short[:])}
	dialer := &net.Dialer{Timeout: 10 * time.Second, LocalAddr: &net.TCPAddr{IP: net.IPv4zero}}
	gateway, err := reality.New(reality.Config{
		TLS: &utls.RealityConfig{Type: "tcp", Dest: net.JoinHostPort(endpoint.ServerName, "443"), DialContext: dialer.DialContext,
			Config:      utls.Config{ServerName: endpoint.ServerName, SessionTicketsDisabled: true},
			ServerNames: map[string]bool{endpoint.ServerName: true}, PrivateKey: key.Bytes(), ShortIds: map[[8]byte]bool{short: true}},
		Resolve: func(context.Context, string) (string, error) {
			return "", errors.New("probe must not authenticate a user")
		},
		Connect: func(context.Context, string, string) (outbound.Client, error) {
			return nil, errors.New("probe must not open a lease")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- gateway.Serve(ctx, listener) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = reality.Handshake(ctx, conn, endpoint); err != nil {
		t.Fatalf("default Reality target %s failed authenticated handshake: %v", endpoint.ServerName, err)
	}
}
