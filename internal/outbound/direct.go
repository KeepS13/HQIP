// Package outbound owns VLESS target sockets, independently of any HY2 process.
package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

// BlockedCIDRs is the managed node egress policy, also rendered into HY2 ACLs.
// Return a new slice so callers cannot mutate the policy used by other protocols.
func BlockedCIDRs() []string {
	return []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8"}
}

// Connector creates unmetered owned sockets; Session applies the shared guard.
// Implementations must enforce egress rules and honor cancellation during dial.
type Connector interface {
	TCP(context.Context, string) (net.Conn, error)
	UDP(context.Context) (nodepolicy.UDPConn, error)
}

// Direct uses the managed node's IPv4, resolver and reject policy. DNS results
// are checked before dialing their numeric IP, avoiding a second DNS lookup.
type Direct struct {
	Source   net.IP
	Resolver *net.Resolver
	blocked  []netip.Prefix
}

func NewDirect(sourceIP string) (*Direct, error) {
	ip := net.ParseIP(sourceIP).To4()
	if ip == nil {
		return nil, errors.New("direct outbound requires node IPv4")
	}
	d := &Direct{Source: ip}
	for _, cidr := range BlockedCIDRs() {
		d.blocked = append(d.blocked, netip.MustParsePrefix(cidr))
	}
	d.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 4 * time.Second}
		if network == "tcp" {
			dialer.LocalAddr = &net.TCPAddr{IP: ip}
		} else {
			dialer.LocalAddr = &net.UDPAddr{IP: ip}
		}
		return dialer.DialContext(ctx, network, "1.1.1.1:53")
	}}
	return d, nil
}

func (d *Direct) destination(ctx context.Context, address string) (*net.UDPAddr, error) {
	targets, err := d.destinations(ctx, address)
	if err != nil {
		return nil, err
	}
	return targets[0], nil
}

// Resolve once, filter every address before dialing and cap fan-out. TCP may
// try other allowed addresses; UDP keeps one target to avoid duplicate packets.
func (d *Direct) destinations(ctx context.Context, address string) ([]*net.UDPAddr, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid destination port")
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip.Unmap()}
	} else {
		lookup, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		resolved, err := d.Resolver.LookupNetIP(lookup, "ip4", host)
		if err != nil {
			return nil, err
		}
		ips = resolved
	}
	targets := make([]*net.UDPAddr, 0, min(len(ips), 16))
	seen := make(map[netip.Addr]bool)
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.Is4() || seen[ip] {
			continue
		}
		seen[ip] = true
		blocked := false
		for _, prefix := range d.blocked {
			blocked = blocked || prefix.Contains(ip)
		}
		if !blocked {
			targets = append(targets, &net.UDPAddr{IP: net.IP(ip.AsSlice()), Port: port})
			if len(targets) == 16 {
				break
			}
		}
	}
	if len(targets) > 0 {
		return targets, nil
	}
	return nil, errors.New("destination rejected by node egress policy")
}

func (d *Direct) TCP(ctx context.Context, address string) (net.Conn, error) {
	// DNS and fallback share one deadline; no application bytes are replayed.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	targets, err := d.destinations(ctx, address)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 10 * time.Second, LocalAddr: &net.TCPAddr{IP: d.Source}}
	return dialTCPAddresses(ctx, targets, dialer.DialContext)
}

func (d *Direct) UDP(ctx context.Context) (nodepolicy.UDPConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: d.Source})
	if err != nil {
		return nil, err
	}
	return &directUDP{UDPConn: conn, ctx: ctx, direct: d, targets: make(map[string]udpTarget)}, nil
}
