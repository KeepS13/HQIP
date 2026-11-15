package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"
)

// TCP establishment alone does not prove an address can carry HTTPS. Some
// networks complete TCP to a CDN address but stall its TLS handshake, so the
// standard TCP-only Happy Eyeballs fallback never tries the working address.
// Race verified TLS handshakes before sending any HTTP request or credentials.
func newDirectHTTPTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ForceAttemptHTTP2 = true
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		config := transport.TLSClientConfig.Clone()
		if config == nil {
			config = &tls.Config{}
		}
		if config.ServerName == "" {
			config.ServerName = host
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		candidates := interleaveAddresses(addresses, port)
		dialer := &tls.Dialer{NetDialer: &net.Dialer{KeepAlive: 30 * time.Second}, Config: config}
		return raceTLSAddresses(ctx, candidates, 250*time.Millisecond, func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		})
	}
	return transport
}

func interleaveAddresses(addresses []net.IPAddr, port string) []string {
	var first, second []string
	if len(addresses) == 0 {
		return nil
	}
	firstIsV4 := addresses[0].IP.To4() != nil
	seen := map[string]bool{}
	for _, address := range addresses {
		value := net.JoinHostPort(address.String(), port)
		if seen[value] {
			continue
		}
		seen[value] = true
		if (address.IP.To4() != nil) == firstIsV4 {
			first = append(first, value)
		} else {
			second = append(second, value)
		}
	}
	result := make([]string, 0, len(first)+len(second))
	for i := 0; i < max(len(first), len(second)); i++ {
		if i < len(first) {
			result = append(result, first[i])
		}
		if i < len(second) {
			result = append(result, second[i])
		}
	}
	return result
}

func raceTLSAddresses(ctx context.Context, addresses []string, delay time.Duration, dial func(context.Context, string) (net.Conn, error)) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("账户服务未解析到可用地址")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	// An unbuffered handoff gives each losing worker ownership of closing its
	// connection, even if it finishes successfully just as another address wins.
	results := make(chan result)
	next, active := 0, 0
	start := func() {
		address := addresses[next]
		next++
		active++
		go func() {
			conn, err := dial(ctx, address)
			select {
			case results <- result{conn, err}:
			case <-ctx.Done():
				if conn != nil {
					conn.Close()
				}
			}
		}()
	}
	start()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var failures []error
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case outcome := <-results:
			active--
			if err := ctx.Err(); err != nil {
				if outcome.conn != nil {
					outcome.conn.Close()
				}
				return nil, err
			}
			if outcome.err == nil {
				return outcome.conn, nil
			}
			if outcome.conn != nil {
				outcome.conn.Close()
			}
			failures = append(failures, outcome.err)
			if next < len(addresses) {
				start()
				timer.Reset(delay)
			} else if active == 0 {
				return nil, errors.Join(failures...)
			}
		case <-timer.C:
			if next < len(addresses) {
				start()
				timer.Reset(delay)
			}
		}
	}
}
