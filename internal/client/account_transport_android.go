//go:build android

package client

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

var mobileNetwork struct {
	sync.RWMutex
	protect func(int) error
	lookup  func(string) (string, error)
}

// SetAndroidSocketProtector is configured by the in-process native host before
// constructing a controller. The callback binds sockets to a non-VPN network;
// a missing protector fails closed, never depending on a working VPN.
func SetAndroidSocketProtector(protect func(int) error) {
	mobileNetwork.Lock()
	defer mobileNetwork.Unlock()
	mobileNetwork.protect = protect
}

// Android DNS must use the same non-VPN Network as the protected socket.
func SetAndroidResolver(lookup func(string) (string, error)) {
	mobileNetwork.Lock()
	defer mobileNetwork.Unlock()
	mobileNetwork.lookup = lookup
}
func lookupAccountIPs(ctx context.Context, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil
	}
	mobileNetwork.RLock()
	lookup := mobileNetwork.lookup
	mobileNetwork.RUnlock()
	if lookup == nil {
		return nil, errors.New("系统直连 DNS 尚未就绪")
	}
	type answer struct {
		value string
		err   error
	}
	result := make(chan answer, 1)
	go func() { value, err := lookup(host); result <- answer{value, err} }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-result:
		if response.err != nil {
			return nil, &net.DNSError{Err: "physical network lookup failed", Name: host}
		}
		ips := []net.IPAddr{}
		for _, value := range strings.Split(response.value, "\n") {
			if ip := net.ParseIP(value); ip != nil {
				ips = append(ips, net.IPAddr{IP: ip})
			}
		}
		if len(ips) == 0 {
			return nil, &net.DNSError{Err: "no usable address", Name: host}
		}
		return ips, nil
	}
}

func directAccountTCP(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	mobileNetwork.RLock()
	protect := mobileNetwork.protect
	mobileNetwork.RUnlock()
	if protect == nil {
		return nil, errors.New("系统直连网络尚未就绪")
	}
	dialer := &net.Dialer{KeepAlive: 30 * time.Second, Control: func(_, _ string, raw syscall.RawConn) error {
		var protected error
		if err := raw.Control(func(fd uintptr) { protected = protect(int(fd)) }); err != nil {
			return err
		}
		return protected
	}}
	return dialer.DialContext(ctx, network, address)
}
