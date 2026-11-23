// Package agent keeps node authentication and metering independent of control-plane
// availability. Credentials use hashes; the reversible Reality backend alias
// cache is encrypted with the node secret and gated by a live local quota grant.
package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/outbound"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

type cachedCredential struct {
	Hash    string `json:"hash"`
	Enabled bool   `json:"enabled"`
}

type pendingRevocation struct {
	Since     int64 `json:"since"`
	ZeroSince int64 `json:"zeroSince"`
}

type cachedState struct {
	NodeQuotaVersion        int                          `json:"nodeQuotaVersion,omitempty"`
	OfflineAdmissionVersion int                          `json:"offlineAdmissionVersion"`
	PortalEnabled           bool                         `json:"portalEnabled"`
	Retirement              *cachedRetirement            `json:"retirement,omitempty"`
	Certificate             *model.CertificateStatus     `json:"certificate,omitempty"`
	DesiredRevision         int64                        `json:"desiredRevision"`
	AppliedRevision         int64                        `json:"appliedRevision"`
	Credentials             map[string]cachedCredential  `json:"credentials"`
	Pending                 map[string]pendingRevocation `json:"pending"`
	Removals                map[string]cachedRemoval     `json:"removals"`
	DeniedHashes            map[string]bool              `json:"deniedHashes"`
	LastRestart             int64                        `json:"lastRestart"`
	Warning                 string                       `json:"warning,omitempty"`
}

type liveState struct {
	Metrics   model.Metrics
	Online    map[string]int
	Healthy   bool
	SampleAt  time.Time
	TrafficAt time.Time
}

type Agent struct {
	directMu        sync.Mutex
	directSessions  map[outbound.Client]struct{}
	directPending   counter
	directSampleAt  time.Time
	realityMu       sync.RWMutex
	realityEndpoint *model.RealityEndpoint
	cfg             model.AgentConfig
	log             *slog.Logger
	ledger          *ledger
	controlClient   *http.Client
	// Owned by syncLoop; configuration/revocation work continues during backoff.
	reportRetryAt time.Time
	reportBackoff time.Duration
	offlineMu     sync.Mutex
	offlineBoot   time.Time
	offlineIssued map[string]time.Time
	// Owned by offlineMu, independent of the external HY2 process.
	realityGeneration string
	admissionMu       sync.Mutex
	leaseReportMu     sync.RWMutex
	statsClient       *http.Client
	cacheMu           sync.RWMutex
	cache             cachedState
	liveMu            sync.RWMutex
	live              liveState
	metrics           systemMetrics
	lifecycleMu       sync.Mutex
	stopCore          func(context.Context) error
	disableAgent      func(context.Context) error
	finishRetirement  func(context.Context) error
	prepareRetirement func(context.Context) error
	sampleMu          sync.Mutex
	generation        func(context.Context) (string, error)
	restartCore       func(context.Context) error
	now               func() time.Time
	drainGrace        time.Duration
	revokeWake        chan struct{}
	closeOnce         sync.Once
	closeErr          error

	// Recovery scheduling is owned by offlineMu. Requests themselves are durable;
	// losing this cursor/backoff on restart only causes an earlier retry.
	offlineRecoveryCursor  string
	offlineRecoveryRetryAt time.Time
}

func New(cfg model.AgentConfig, logger *slog.Logger) (*Agent, error) {
	if err := normalizeConfig(&cfg); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	l, err := openLedger(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	// Direct sessions cannot survive this Agent process. Their already-recorded
	// raw usage remains intact; orphan telemetry baselines are no longer needed.
	if _, err = l.db.Exec("DELETE FROM baseline WHERE generation='direct'"); err != nil {
		_ = l.close()
		return nil, err
	}
	cached, err := l.loadCache()
	if err != nil {
		_ = l.close()
		return nil, err
	}
	// A pending revoke must re-establish an empty-session observation after restart.
	for id, pending := range cached.Pending {
		pending.ZeroSince = 0
		cached.Pending[id] = pending
	}
	noRedirect := func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	a := &Agent{
		offlineBoot: time.Now(), offlineIssued: map[string]time.Time{},
		cfg: cfg, log: logger, ledger: l, cache: cached,
		controlClient: &http.Client{Timeout: 4 * time.Second, CheckRedirect: noRedirect},
		statsClient: &http.Client{
			Timeout: 600 * time.Millisecond, CheckRedirect: noRedirect,
			Transport: &http.Transport{Proxy: nil, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second},
		},
		now: time.Now, drainGrace: 2 * time.Second, revokeWake: make(chan struct{}, 1),
	}
	a.generation = a.systemdGeneration
	a.restartCore = a.systemdRestart
	a.stopCore = a.stopOwnedCore
	a.disableAgent = a.disableOwnedAgent
	a.finishRetirement = a.finishOwnedRetirement
	a.prepareRetirement = a.prepareOwnedRetirement
	return a, nil
}

// Run serves loopback authentication and runs independent sampling, control-plane
// synchronization, and revocation loops. Slow or unreachable WAN requests never
// hold up the one-second accounting loop.
func (a *Agent) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("监听鉴权接口失败: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Handler: a.Handler(), ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: 3 * time.Second, WriteTimeout: admissionWriteTimeout,
		IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 * 1024,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	var wg sync.WaitGroup
	for _, loop := range []func(context.Context){a.sampleLoop, a.syncLoop, a.revokeLoop, a.maintenanceLoop, a.realityLoop} {
		wg.Add(1)
		go func(loop func(context.Context)) { defer wg.Done(); loop(ctx) }(loop)
	}
	a.log.Info("节点 Agent 已启动", "listen", a.cfg.Listen)
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	}
	cancel()
	// Direct sessions must deliver finals while the local admission listener is
	// still available. Each session's final has its own bounded background context.
	wg.Wait()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	return runErr
}

func (a *Agent) Close() error {
	a.closeOnce.Do(func() {
		a.controlClient.CloseIdleConnections()
		a.statsClient.CloseIdleConnections()
		a.closeErr = a.ledger.close()
	})
	return a.closeErr
}
