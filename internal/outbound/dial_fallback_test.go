package outbound

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A local DNS fixture returns a refused address before the working one. This
// exercises actual resolution and TCP dialing without using public services.
func TestDirectTCPUsesOtherResolvedAddress(t *testing.T) {
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	dns, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dns.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, peer, err := dns.ReadFrom(buf)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(buf[:n]) != nil {
				continue
			}
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionAvailable: true}, Questions: query.Questions}
			for _, question := range query.Questions {
				if question.Type != dnsmessage.TypeA {
					continue
				}
				for _, ip := range [][4]byte{{127, 0, 0, 3}, {127, 0, 0, 1}} {
					response.Answers = append(response.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30},
						Body:   &dnsmessage.AResource{A: ip},
					})
				}
			}
			raw, _ := response.Pack()
			_, _ = dns.WriteTo(raw, peer)
		}
	}()
	d, _ := NewDirect("127.0.0.2")
	d.blocked = nil // Only this isolated loopback fixture bypasses egress policy.
	d.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp4", dns.LocalAddr().String())
	}}
	_, port, _ := net.SplitHostPort(target.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	c, err := d.TCP(ctx, net.JoinHostPort("multi.test", port))
	if err != nil {
		t.Fatalf("healthy address was never reached: %v", err)
	}
	defer c.Close()
	if c.LocalAddr().(*net.TCPAddr).IP.String() != "127.0.0.2" || c.RemoteAddr().String() != target.Addr().String() {
		t.Fatal("fallback changed source binding or target", c.LocalAddr(), c.RemoteAddr())
	}
}
