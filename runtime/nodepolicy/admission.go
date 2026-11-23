// Package nodepolicy owns protocol-neutral forwarding authorization and metering.
// It has no dependency on HY2, QUIC, Reality or a particular outbound transport.
package nodepolicy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/KeepS13/HQIP/runtime/nodepolicy/billing"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AdmissionConfig struct {
	URL    string `mapstructure:"url"`
	Secret string `mapstructure:"secret"`
	// Transport identifies the authenticated local caller, never an end-client field.
	Transport string `mapstructure:"-"`
}

type LeaseGrant struct {
	Authenticated           bool           `json:"authenticated,omitempty"` // Identity only; never a forwarding grant.
	Billing                 *billing.Grant `json:"billing,omitempty"`
	LeaseID                 string         `json:"leaseId"`
	UserID                  string         `json:"userId"`
	AccountingID            string         `json:"accountingId"`
	ExpiresAt               time.Time      `json:"expiresAt"`
	BandwidthBytesPerSecond int64          `json:"bandwidthBytesPerSecond"`
	BudgetBytes             int64          `json:"budgetBytes"`
	ConsumedBytes           int64          `json:"consumedBytes"`
	Revision                int64          `json:"revision"`
	RateVersion             int64          `json:"rateVersion"`
	RatePending             bool           `json:"ratePending"`
	Legacy                  bool           `json:"legacy,omitempty"`
}

type leaseUsage struct {
	Billing            []billing.Segment `json:"billing,omitempty"`
	UploadBytes        int64             `json:"uploadBytes"`
	DownloadBytes      int64             `json:"downloadBytes"`
	Closed             bool              `json:"closed"`
	AppliedRateVersion int64             `json:"appliedRateVersion"`
}

type Admission struct {
	config AdmissionConfig
	client *http.Client
}

func NewAdmission(config AdmissionConfig) (*Admission, error) {
	u, err := url.Parse(config.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("admission requires loopback HTTP root")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || len(config.Secret) < 24 {
		return nil, errors.New("invalid local admission endpoint or secret")
	}
	config.URL = strings.TrimRight(config.URL, "/")
	// Agent control requests allow four seconds and its response write deadline
	// is six seconds. Do not abandon a successful grant before it can be flushed.
	return &Admission{config: config, client: &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type admissionError struct{ denied bool }

func (e admissionError) Error() string { return "HQIP admission unavailable or denied" }

func (a *Admission) request(ctx context.Context, path string, body any) (LeaseGrant, error) {
	var grant LeaseGrant
	data, err := json.Marshal(body)
	if err != nil {
		return grant, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.URL+path, bytes.NewReader(data))
	if err != nil {
		return grant, err
	}
	req.Header.Set("Authorization", "Bearer "+a.config.Secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return grant, admissionError{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return grant, admissionError{denied: resp.StatusCode >= 400 && resp.StatusCode < 500}
	}
	// A complete JSON object can precede a truncated HTTP chunk/body. Wait for
	// transport completion before using a grant the Agent may have failed to send.
	response, err := io.ReadAll(io.LimitReader(resp.Body, (16<<10)+1))
	if err != nil || len(response) > 16<<10 || json.Unmarshal(response, &grant) != nil {
		return grant, admissionError{denied: true}
	}
	return grant, nil
}

func randomConnectionID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (a *Admission) open(ctx context.Context, connectionID, password, clientIP string, closeConnection func()) (*connectionLease, string, error) {
	start := time.Now()
	// The ingress supplies the peer address; subscription contents and auth
	// headers never choose the IP used by the shared online limit.
	g, err := a.request(ctx, "/admission", map[string]string{"connectionId": connectionID, "password": password, "clientIP": clientIP, "billingCapability": billing.Capability, "transport": a.config.Transport})
	if err != nil {
		return nil, "", err
	}
	// Identity-only receipts must never turn into a forwarding permit, even
	// if a malformed response also carries the legacy compatibility marker.
	if g.Authenticated {
		return nil, "", errors.New("identity receipt is not a forwarding grant")
	}
	if g.Legacy {
		if g.AccountingID == "" || g.LeaseID != "" {
			return nil, "", errors.New("invalid legacy grant")
		}
	}
	legacy := g.Legacy
	if legacy {
		g = legacyGrant(connectionID, g.AccountingID, start)
	}
	ctx, cancel := context.WithCancel(ctx)
	l := &connectionLease{admission: a, ctx: ctx, cancel: cancel, closeConnection: closeConnection, wake: make(chan struct{}, 1), rateWake: make(chan struct{}), grant: g}
	if legacy {
		l.legacyPassword = password
	}
	if err = l.acceptGrant(g, start, true); err != nil {
		cancel()
		return nil, "", err
	}
	go l.run()
	return l, g.AccountingID, nil
}
