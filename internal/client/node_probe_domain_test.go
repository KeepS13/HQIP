package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// localhost exercises the published domain-address format without external
// DNS. Reaching each loopback listener proves the probe did not reject the
// address before attempting its authenticated protocol handshake.
func TestNodeProbeDomainReachesTransport(t *testing.T) {
	t.Run("reality", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		reached := make(chan struct{})
		go func() {
			conn, err := listener.Accept()
			if err == nil {
				conn.Close()
				close(reached)
			}
		}()
		port := listener.Addr().(*net.TCPAddr).Port
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err = probeNodeHandshake(ctx, Node{Server: "localhost", Port: port, SNI: "node.test", Protocol: "vless", Reality: model.RealityEndpoint{Port: port, ServerName: "node.test"}})
		if err == nil {
			t.Fatal("unauthenticated listener passed Reality verification")
		}
		select {
		case <-reached:
		case <-ctx.Done():
			// Dial can complete before the listener goroutine is scheduled.
			// Wait within the existing budget instead of testing that race.
			t.Fatalf("domain did not reach TCP listener: %v", err)
		}
	})
	t.Run("hysteria2", func(t *testing.T) {
		listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		reached := make(chan struct{})
		go func() {
			buffer := make([]byte, 2048)
			if _, _, err := listener.ReadFrom(buffer); err == nil {
				close(reached)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		_, err = probeNodeHandshake(ctx, Node{Server: "localhost", Port: listener.LocalAddr().(*net.UDPAddr).Port, SNI: "node.test", Protocol: "hysteria2"})
		if err == nil {
			t.Fatal("silent listener passed QUIC verification")
		}
		select {
		case <-reached:
		default:
			t.Fatalf("domain did not reach UDP listener: %v", err)
		}
	})
}
