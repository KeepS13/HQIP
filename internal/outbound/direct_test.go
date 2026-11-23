package outbound

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestDirectRejectsPrivateLiteralDNSAndIPv6(t *testing.T) {
	d, err := NewDirect("127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	d.Resolver = net.DefaultResolver
	for _, target := range []string{"127.0.0.1:80", "10.1.2.3:80", "100.64.0.1:80", "169.254.169.254:80", "localhost:80", "[::ffff:127.0.0.1]:80", "[::1]:80", "8.8.8.8:0"} {
		if _, err := d.destination(context.Background(), target); err == nil {
			t.Fatal("unsafe target allowed", target)
		}
	}
	if addr, err := d.destination(context.Background(), "8.8.8.8:53"); err != nil || addr.String() != "8.8.8.8:53" {
		t.Fatal("public destination rejected", addr, err)
	}
}

func TestDirectTCPAndUDPBindNodeIPv4(t *testing.T) {
	d, _ := NewDirect("127.0.0.2")
	d.blocked = nil // Test fixture only; production constructors always install ACLs.
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	peer := make(chan string, 1)
	go func() {
		c, e := target.Accept()
		if e == nil {
			peer <- c.RemoteAddr().(*net.TCPAddr).IP.String()
			_, _ = io.Copy(c, c)
			_ = c.Close()
		}
	}()
	c, err := d.TCP(context.Background(), target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := <-peer; got != "127.0.0.2" {
		t.Fatal("TCP escaped node IPv4", got)
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	u, err := d.UDP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if _, err = u.WriteTo([]byte("payload"), udp.LocalAddr().String()); err != nil {
		t.Fatal(err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 32)
	n, from, err := udp.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "payload" || from.IP.String() != "127.0.0.2" {
		t.Fatal("UDP escaped node IPv4", from, err)
	}
	_, _ = udp.WriteToUDP(buf[:n], from)
	n, _, err = u.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "payload" {
		t.Fatal("UDP response", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if conn, err := d.TCP(ctx, target.Addr().String()); err == nil {
		conn.Close()
		t.Fatal("cancelled TCP dial succeeded")
	}
}
