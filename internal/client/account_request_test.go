package client

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type accountTransportFunc func(*http.Request) (*http.Response, error)

func (f accountTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccountTransportRetriesOnlyReplaySafeOperations(t *testing.T) {
	for _, test := range []struct {
		method, path string
		attempts     int
	}{
		{"GET", "/api/v3/client/profile", 2},
		{"POST", "/api/v3/client/refresh", 1},
		{"POST", "/api/v3/client/token", 1},
		{"POST", "/api/v3/client/subscription", 1},
		{"POST", "/api/v3/client/logout", 1},
	} {
		t.Run(test.path, func(t *testing.T) {
			calls := 0
			h := &http.Client{Transport: accountTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(r.Body)
				r.Body.Close()
				if string(body) != `{"requestId":"stable"}` || r.Header.Get("Authorization") != "Bearer secret" {
					t.Fatal("replay changed request")
				}
				if calls == 1 {
					return nil, io.EOF
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})}
			req, _ := http.NewRequest(test.method, "https://example.invalid"+test.path, strings.NewReader(`{"requestId":"stable"}`))
			req.Header.Set("Authorization", "Bearer secret")
			response, err := accountRequest(h, req)
			if response != nil {
				response.Body.Close()
			}
			if calls != test.attempts || (test.attempts == 2 && err != nil) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAccountTransportDoesNotReplayCertificateOrHTTPFailures(t *testing.T) {
	for _, status := range []int{401, 403, 409, 429, 503, 0} {
		calls := 0
		h := &http.Client{Transport: accountTransportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if status == 0 {
				return nil, &tls.CertificateVerificationError{Err: errors.New("untrusted")}
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})}
		req, _ := http.NewRequest("GET", "https://example.invalid/api/v3/client/profile", nil)
		response, _ := accountRequest(h, req)
		if response != nil {
			response.Body.Close()
		}
		if calls != 1 {
			t.Fatal("replayed HTTP/certificate failure", status)
		}
	}
}

func TestAccountTransportCancellationAndCredentialSafeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	h := &http.Client{Transport: accountTransportFunc(func(r *http.Request) (*http.Response, error) { calls++; cancel(); return nil, io.EOF })}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://example.invalid/api/v3/client/profile", nil)
	_, _ = accountRequest(h, req)
	if calls != 1 {
		t.Fatal("cancelled operation replayed")
	}
	for _, cause := range []error{&net.DNSError{Err: "secret", Name: "secret"}, &net.DNSError{IsTimeout: true}, io.EOF} {
		err := accountTransportError(cause)
		if !retryable(err) || errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe network classification", err)
		}
	}
}
