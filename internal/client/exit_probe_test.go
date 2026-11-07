package client

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExitProbeFallsBackAfterProviderTimeoutWithTLSVerification(t *testing.T) {
	var first, second atomic.Int32
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		<-r.Context().Done()
	}))
	defer slow.Close()
	healthy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		_, _ = io.WriteString(w, "203.0.113.9\n")
	}))
	defer healthy.Close()
	h := healthy.Client()
	h.Timeout = 100 * time.Millisecond
	err := probeExit(context.Background(), h, "203.0.113.9", []exitProbeTarget{{"primary", slow.URL, true}, {"secondary", healthy.URL, false}})
	if err != nil || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("same-node provider fallback: %v, requests=%d/%d", err, first.Load(), second.Load())
	}
	if h.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("probe disabled TLS validation")
	}
}

func TestExitProbeMismatchDoesNotTryAnotherProvider(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "ip=203.0.113.10\n")
	}))
	defer s.Close()
	err := probeExit(context.Background(), s.Client(), "203.0.113.9", []exitProbeTarget{{"primary", s.URL, true}, {"secondary", s.URL, true}})
	if !errors.Is(err, errExitMismatch) || requests.Load() != 1 {
		t.Fatalf("unexpected exit must stop immediately: %v requests=%d", err, requests.Load())
	}
}

func TestExitProbeCancellationDoesNotStartFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		cancel()
		<-r.Context().Done()
	}))
	defer s.Close()
	err := probeExit(ctx, s.Client(), "203.0.113.9", []exitProbeTarget{{"first", s.URL, true}, {"second", s.URL, false}})
	if !errors.Is(err, context.Canceled) || requests.Load() != 1 {
		t.Fatalf("cancelled connection continued: %v requests=%d", err, requests.Load())
	}
}

func TestExitProbeRejectsUntrustedTLS(t *testing.T) {
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "203.0.113.9")
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	defer s.Close()
	h := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	defer h.CloseIdleConnections()
	err := probeExit(context.Background(), h, "203.0.113.9", []exitProbeTarget{{"untrusted", s.URL, false}})
	if err == nil || !strings.Contains(err.Error(), "证书校验失败") {
		t.Fatalf("untrusted TLS accepted or misreported: %v", err)
	}
}

func TestExitProbeResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body, expected string
		trace, valid         bool
		status               int
	}{
		{"trace", "fl=test\nip=203.0.113.9\n", "203.0.113.9", true, true, 200},
		{"ipv6", "2001:db8::1\n", "2001:db8:0::1", false, true, 200},
		{"duplicate", "ip=203.0.113.9\nip=203.0.113.9\n", "203.0.113.9", true, false, 200},
		{"invalid IP", "ip=not-an-ip\n", "", true, false, 200},
		{"invalid expected", "203.0.113.9", "bad", false, false, 200},
		{"oversize", "ip=203.0.113.9\n" + strings.Repeat("x", 32<<10), "203.0.113.9", true, false, 200},
		{"failure status", "secret body", "203.0.113.9", false, false, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer s.Close()
			err := probeExit(context.Background(), s.Client(), tc.expected, []exitProbeTarget{{"test", s.URL, tc.trace}})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret body") {
				t.Fatal("error exposed provider response")
			}
		})
	}
}

func TestProbeCancelledBeforeLocalPortDoesNotWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := Probe(ctx, 1, "203.0.113.9"); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("cancelled probe waited or lost cancellation: %v", err)
	}
}
