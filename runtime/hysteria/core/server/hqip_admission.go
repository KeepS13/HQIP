package server

// HQIP extension: leases are bound to one real QUIC connection. The wire
// protocol remains Hysteria 2; clients cannot select a lease or bypass guards.
import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type AdmissionConfig struct {
	URL    string `mapstructure:"url"`
	Secret string `mapstructure:"secret"`
}

type LeaseGrant struct {
	LeaseID                 string    `json:"leaseId"`
	UserID                  string    `json:"userId"`
	AccountingID            string    `json:"accountingId"`
	ExpiresAt               time.Time `json:"expiresAt"`
	BandwidthBytesPerSecond int64     `json:"bandwidthBytesPerSecond"`
	BudgetBytes             int64     `json:"budgetBytes"`
	ConsumedBytes           int64     `json:"consumedBytes"`
	Revision                int64     `json:"revision"`
	RateVersion             int64     `json:"rateVersion"`
	RatePending             bool      `json:"ratePending"`
	Legacy                  bool      `json:"legacy,omitempty"`
}

type leaseUsage struct {
	UploadBytes        int64 `json:"uploadBytes"`
	DownloadBytes      int64 `json:"downloadBytes"`
	Closed             bool  `json:"closed"`
	AppliedRateVersion int64 `json:"appliedRateVersion"`
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

func (a *Admission) open(ctx context.Context, connectionID, password string, closeConnection func()) (*connectionLease, string, error) {
	start := time.Now()
	g, err := a.request(ctx, "/admission", map[string]string{"connectionId": connectionID, "password": password})
	if err != nil {
		return nil, "", err
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

type connectionLease struct {
	admission        *Admission
	ctx              context.Context
	cancel           context.CancelFunc
	closeConnection  func()
	mu               sync.Mutex
	renewMu          sync.Mutex
	grant            LeaseGrant
	deadline         time.Time // Contains monotonic clock; never recomputed from wall clock on read.
	usedUp, usedDown int64
	nextUpload       time.Time
	nextDownload     time.Time
	closed           bool
	wake             chan struct{}
	rateWake         chan struct{}
	finishOnce       sync.Once
	legacyPassword   string // Rechecked locally only, never sent centrally on renewal.
}

func legacyGrant(connectionID, accountingID string, start time.Time) LeaseGrant {
	return LeaseGrant{LeaseID: connectionID, UserID: "legacy", AccountingID: accountingID, ExpiresAt: start.Add(120 * time.Second), BudgetBytes: math.MaxInt64}
}

func (l *connectionLease) acceptGrant(g LeaseGrant, requestStart time.Time, initial bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.closed || g.Legacy || g.LeaseID == "" || g.AccountingID == "" || g.UserID == "" || g.BandwidthBytesPerSecond < 0 || g.BudgetBytes <= 0 || g.ConsumedBytes < 0 || g.ConsumedBytes > g.BudgetBytes || g.Revision < 0 {
		return errors.New("invalid lease grant")
	}
	if g.RateVersion < 0 || (!initial && !now.Before(l.deadline)) {
		return errors.New("invalid rate version or expired lease")
	}
	if !initial && (g.LeaseID != l.grant.LeaseID || g.AccountingID != l.grant.AccountingID || g.UserID != l.grant.UserID || g.Revision < l.grant.Revision || g.BudgetBytes < l.grant.BudgetBytes || g.ConsumedBytes > l.usedUp+l.usedDown || g.ConsumedBytes < l.grant.ConsumedBytes || g.RateVersion < l.grant.RateVersion) {
		return errors.New("non-monotonic lease grant")
	}
	if !initial && g.RateVersion == l.grant.RateVersion && (g.RatePending != l.grant.RatePending || g.BandwidthBytesPerSecond != l.grant.BandwidthBytesPerSecond) {
		return errors.New("rate changed without a new version")
	}
	if initial && g.ConsumedBytes != 0 {
		return errors.New("connection lease cannot be resumed")
	}
	remaining := g.ExpiresAt.Sub(now)
	// Network time is included in the 120-second local authorization ceiling.
	ceiling := requestStart.Add(120 * time.Second).Sub(now)
	if remaining > ceiling {
		remaining = ceiling
	}
	if remaining <= 0 {
		return errors.New("lease expired")
	}
	l.deadline = now.Add(remaining)
	if g.RateVersion != l.grant.RateVersion || g.BandwidthBytesPerSecond != l.grant.BandwidthBytesPerSecond || g.RatePending != l.grant.RatePending {
		close(l.rateWake)
		l.rateWake = make(chan struct{})
		l.nextUpload, l.nextDownload = now, now
	}
	l.grant = g
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return nil
}

func (l *connectionLease) fail() {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		l.cancel()
		l.closeConnection()
	}
	l.mu.Unlock()
}

func (l *connectionLease) renew() error {
	l.renewMu.Lock()
	defer l.renewMu.Unlock()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errors.New("closed lease")
	}
	id := l.grant.LeaseID
	usage := leaseUsage{UploadBytes: l.usedUp, DownloadBytes: l.usedDown, AppliedRateVersion: l.grant.RateVersion}
	deadline := l.deadline
	l.mu.Unlock()
	if !time.Now().Before(deadline) {
		l.fail()
		return errors.New("lease expired")
	}
	requestCtx, cancel := context.WithDeadline(l.ctx, deadline)
	defer cancel()
	start := time.Now()
	var g LeaseGrant
	var err error
	if l.legacyPassword != "" {
		g, err = l.admission.request(requestCtx, "/admission", map[string]string{"connectionId": id, "password": l.legacyPassword, "legacyRenewal": "true"})
		if err == nil {
			if !g.Legacy || g.AccountingID != l.grant.AccountingID || g.LeaseID != "" {
				l.fail()
				return errors.New("legacy authorization ended")
			}
			g = legacyGrant(id, g.AccountingID, start)
		}
	} else {
		g, err = l.admission.request(requestCtx, "/leases/"+url.PathEscape(id)+"/renew", usage)
	}
	if err != nil {
		var ae admissionError
		if errors.As(err, &ae) && ae.denied {
			l.fail()
		}
		return err
	}
	if err = l.acceptGrant(g, start, false); err != nil {
		l.fail()
		return err
	}
	return nil
}

func (l *connectionLease) run() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		l.mu.Lock()
		remaining := time.Until(l.deadline)
		l.mu.Unlock()
		if remaining <= 0 {
			l.fail()
			return
		}
		timer := time.NewTimer(remaining)
		select {
		case <-l.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			l.fail()
			return
		case <-l.wake:
			timer.Stop()
		case <-ticker.C:
			timer.Stop()
			_ = l.renew()
		}
	}
}

// consume paces each direction independently and shares the cumulative budget.
// A failed write is charged conservatively; no payload exceeds the reserved cap.
func (l *connectionLease) consume(upload bool, n int) error {
	if l == nil || n == 0 {
		return nil
	}
	if n < 0 {
		return errors.New("negative payload")
	}
	for n > 0 {
		chunk := n
		if chunk > 16<<10 {
			chunk = 16 << 10
		}
		l.mu.Lock()
		rate := l.grant.BandwidthBytesPerSecond
		l.mu.Unlock()
		if rate > 0 {
			pacedChunk := rate / 10
			if pacedChunk < 1 {
				pacedChunk = 1
			}
			if int64(chunk) > pacedChunk {
				chunk = int(pacedChunk)
			}
		}
		if err := l.consumeChunk(upload, int64(chunk)); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

func (l *connectionLease) consumeChunk(upload bool, n int64) error {
	for {
		l.mu.Lock()
		if l.closed || !time.Now().Before(l.deadline) {
			l.mu.Unlock()
			l.fail()
			return errors.New("lease expired or closed")
		}
		if l.grant.RatePending {
			wake := l.rateWake
			deadline := l.deadline
			l.mu.Unlock()
			timer := time.NewTimer(time.Until(deadline))
			select {
			case <-l.ctx.Done():
				timer.Stop()
				return errors.New("lease closed")
			case <-wake:
				timer.Stop()
				continue
			case <-timer.C:
				continue
			}
		}
		if rate := l.grant.BandwidthBytesPerSecond; rate > 0 {
			chunk := rate / 10
			if chunk < 1 {
				chunk = 1
			}
			if n > chunk {
				l.mu.Unlock()
				return l.consume(upload, int(n))
			}
		}
		if n > l.grant.BudgetBytes-l.usedUp-l.usedDown {
			l.mu.Unlock()
			if err := l.renew(); err != nil {
				return err
			}
			l.mu.Lock()
			enough := n <= l.grant.BudgetBytes-l.usedUp-l.usedDown
			l.mu.Unlock()
			if !enough {
				l.fail()
				return errors.New("lease budget exhausted")
			}
			continue
		}
		rate := l.grant.BandwidthBytesPerSecond
		now := time.Now()
		wait := time.Duration(0)
		wake := l.rateWake
		rateVersion := l.grant.RateVersion
		if rate > 0 {
			next := &l.nextDownload
			if upload {
				next = &l.nextUpload
			}
			if next.Before(now) {
				*next = now
			}
			duration := time.Duration(math.Ceil(float64(n) * float64(time.Second) / float64(rate)))
			if duration < 0 || duration > 120*time.Second {
				l.mu.Unlock()
				return errors.New("payload rate exceeds lease window")
			}
			*next = next.Add(duration)
			wait = next.Sub(now)
		}
		deadline := l.deadline
		l.mu.Unlock()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-l.ctx.Done():
				timer.Stop()
				return errors.New("lease closed")
			case <-wake:
				timer.Stop()
				continue
			case <-timer.C:
			}
		}
		l.mu.Lock()
		if l.grant.RateVersion != rateVersion || l.grant.RatePending {
			l.mu.Unlock()
			continue
		}
		if l.closed || !time.Now().Before(l.deadline) || !time.Now().Before(deadline) {
			l.mu.Unlock()
			l.fail()
			return errors.New("lease expired")
		}
		if n > l.grant.BudgetBytes-l.usedUp-l.usedDown {
			l.mu.Unlock()
			continue
		}
		if upload {
			l.usedUp += n
		} else {
			l.usedDown += n
		}
		l.mu.Unlock()
		return nil
	}
}

func (l *connectionLease) finish() {
	if l == nil {
		return
	}
	l.finishOnce.Do(func() {
		l.fail()
		if l.legacyPassword != "" {
			return
		}
		l.renewMu.Lock()
		defer l.renewMu.Unlock()
		l.mu.Lock()
		id := l.grant.LeaseID
		usage := leaseUsage{UploadBytes: l.usedUp, DownloadBytes: l.usedDown, Closed: true, AppliedRateVersion: l.grant.RateVersion}
		l.mu.Unlock()
		// The Agent durably queues a received final before central delivery.
		// No reservation may be reused if the Agent never received this report.
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_, _ = l.admission.request(ctx, "/leases/"+url.PathEscape(id)+"/renew", usage)
	})
}
