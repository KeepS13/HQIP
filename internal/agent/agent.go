// Package agent keeps node authentication and metering independent of control-plane
// availability. All locally persisted credentials are hashes of random passwords.
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
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
	DesiredRevision int64                        `json:"desiredRevision"`
	AppliedRevision int64                        `json:"appliedRevision"`
	Credentials     map[string]cachedCredential  `json:"credentials"`
	Pending         map[string]pendingRevocation `json:"pending"`
}

type liveState struct {
	Metrics   model.Metrics
	Online    map[string]int
	Healthy   bool
	SampleAt  time.Time
	TrafficAt time.Time
}

type Agent struct {
	cfg           model.AgentConfig
	log           *slog.Logger
	ledger        *ledger
	controlClient *http.Client
	statsClient   *http.Client
	cacheMu       sync.RWMutex
	cache         cachedState
	liveMu        sync.RWMutex
	live          liveState
	metrics       systemMetrics
	generation    func(context.Context) (string, error)
	now           func() time.Time
	drainGrace    time.Duration
	revokeWake    chan struct{}
	closeOnce     sync.Once
	closeErr      error
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
		cfg: cfg, log: logger, ledger: l, cache: cached,
		controlClient: &http.Client{Timeout: 4 * time.Second, CheckRedirect: noRedirect},
		statsClient: &http.Client{
			Timeout: 600 * time.Millisecond, CheckRedirect: noRedirect,
			Transport: &http.Transport{Proxy: nil, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second},
		},
		now: time.Now, drainGrace: 2 * time.Second, revokeWake: make(chan struct{}, 1),
	}
	a.generation = a.systemdGeneration
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
		ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
		IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 * 1024,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	var wg sync.WaitGroup
	for _, loop := range []func(context.Context){a.sampleLoop, a.syncLoop, a.revokeLoop, a.maintenanceLoop} {
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
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	wg.Wait()
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

func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth", a.authenticate)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if !a.localAuthorized(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		a.liveMu.RLock()
		healthy := a.live.Healthy && a.now().Sub(a.live.SampleAt) < 3*time.Second
		a.liveMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"coreHealthy": healthy})
	})
	return mux
}

func (a *Agent) localAuthorized(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !isLoopback(host) {
		return false
	}
	secret := r.URL.Query().Get("secret")
	if secret == "" {
		secret = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(a.cfg.StatsSecret)) == 1
}

func (a *Agent) authenticate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !a.localAuthorized(r) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false}`)
		return
	}
	var input struct {
		Address string `json:"addr"`
		Auth    string `json:"auth"`
		TX      uint64 `json:"tx"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil || len(input.Auth) > 1024 || len(input.Auth) < 16 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false}`)
		return
	}
	hash := passwordHash(input.Auth)
	result := struct {
		OK bool   `json:"ok"`
		ID string `json:"id,omitempty"`
	}{}
	a.cacheMu.RLock()
	for id, credential := range a.cache.Credentials {
		if _, denied := a.cache.Pending[id]; denied || !credential.Enabled {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(credential.Hash)) == 1 {
			result.OK, result.ID = true, id
			break
		}
	}
	// Hold the read lock through writing the authorization decision so applying
	// revocation cannot race an auth handler that has not sent its response yet.
	_ = json.NewEncoder(w).Encode(result)
	a.cacheMu.RUnlock()
}

func passwordHash(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

func (a *Agent) applyDesired(desired model.AgentDesired) error {
	if desired.Revision < 0 || len(desired.Devices) > 10000 {
		return errors.New("控制服务配置无效")
	}
	credentials := make(map[string]cachedCredential, len(desired.Devices))
	seenHashes := make(map[string]bool, len(desired.Devices))
	for _, device := range desired.Devices {
		if len(device.ID) == 0 || len(device.ID) > 128 || len(device.Password) < 16 || len(device.Password) > 1024 {
			return errors.New("控制服务返回无效设备凭据")
		}
		hash := passwordHash(device.Password)
		if _, duplicate := credentials[device.ID]; duplicate || seenHashes[hash] {
			return errors.New("控制服务返回重复设备凭据")
		}
		credentials[device.ID] = cachedCredential{Hash: hash, Enabled: device.Enabled}
		seenHashes[hash] = true
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if desired.Revision <= a.cache.DesiredRevision {
		return nil
	}
	replay := desired.Revision-a.cache.DesiredRevision > 1
	next := cachedState{
		DesiredRevision: desired.Revision, AppliedRevision: a.cache.AppliedRevision,
		Credentials: credentials, Pending: make(map[string]pendingRevocation),
	}
	for id, pending := range a.cache.Pending {
		next.Pending[id] = pending
	}
	for id, old := range a.cache.Credentials {
		current, exists := credentials[id]
		if old.Enabled && (!exists || !current.Enabled || current.Hash != old.Hash) {
			next.Pending[id] = pendingRevocation{Since: a.now().UnixNano()}
		}
	}
	if len(next.Pending) == 0 {
		next.AppliedRevision = desired.Revision
	}
	if err := a.ledger.saveCache(next); err != nil {
		// Fail closed for newly revoked credentials even if local storage is full.
		for id, pending := range next.Pending {
			a.cache.Pending[id] = pending
		}
		return err
	}
	a.cache = next
	if replay {
		// Restore increments the revision epoch. Re-send already acknowledged
		// counters immediately so the restored control DB catches up from this node.
		if err := a.ledger.replay(a.now()); err != nil {
			return err
		}
	}
	select {
	case a.revokeWake <- struct{}{}:
	default:
	}
	return nil
}

func (a *Agent) systemdGeneration(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", "show", "--property=InvocationID", "--value", a.cfg.HysteriaService)
	output, err := cmd.Output()
	if err != nil {
		return "", errors.New("无法获取 Hysteria systemd 运行代次")
	}
	id := strings.TrimSpace(string(output))
	if len(id) != 32 {
		return "", errors.New("Hysteria 尚未启动")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", errors.New("Hysteria 运行代次无效")
	}
	return id, nil
}

func (a *Agent) statsJSON(ctx context.Context, method, path string, body any, output any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.StatsURL+path, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("统计接口请求地址无效")
	}
	req.Header.Set("Authorization", a.cfg.StatsSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.statsClient.Do(req)
	if err != nil {
		return errors.New("无法连接本机 Hysteria 统计接口")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("Hysteria 统计接口返回 HTTP %d", resp.StatusCode)
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(output); err != nil {
			return errors.New("Hysteria 统计数据无效")
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	}
	return nil
}

func (a *Agent) sample(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	generation, err := a.generation(ctx)
	if err != nil {
		a.sampleFailed()
		return err
	}
	var counters map[string]counter
	var online map[string]int
	var trafficErr, onlineErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); trafficErr = a.statsJSON(ctx, http.MethodGet, "/traffic", nil, &counters) }()
	go func() { defer wg.Done(); onlineErr = a.statsJSON(ctx, http.MethodGet, "/online", nil, &online) }()
	wg.Wait()
	endGeneration, generationErr := a.generation(ctx)
	if generationErr != nil || generation != endGeneration {
		a.sampleFailed()
		return errors.New("Hysteria 正在重启，跳过不一致采样")
	}
	if trafficErr != nil || counters == nil {
		a.sampleFailed()
		if trafficErr != nil {
			return trafficErr
		}
		return errors.New("Hysteria 流量接口返回空数据")
	}
	now := a.now()
	upload, download, err := a.ledger.record(ctx, now, generation, counters)
	if err != nil {
		a.sampleFailed()
		return err
	}
	metrics := a.metrics.read(a.cfg.StatePath)
	for _, n := range online {
		if n < 0 {
			onlineErr = errors.New("Hysteria 在线连接计数无效")
		}
	}
	a.liveMu.Lock()
	if !a.live.TrafficAt.IsZero() {
		seconds := now.Sub(a.live.TrafficAt).Seconds()
		if seconds > 0 {
			metrics.UploadRate = float64(upload) / seconds
			metrics.DownloadRate = float64(download) / seconds
		}
	}
	a.live.Metrics = metrics
	a.live.TrafficAt = now
	a.live.SampleAt = now
	a.live.Healthy = onlineErr == nil && online != nil
	if a.live.Healthy {
		a.live.Online = online
	}
	a.liveMu.Unlock()
	return onlineErr
}

func (a *Agent) sampleFailed() {
	a.liveMu.Lock()
	a.live.Healthy = false
	a.live.Metrics.UploadRate, a.live.Metrics.DownloadRate = 0, 0
	a.liveMu.Unlock()
}

func (a *Agent) sampleLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastLog := time.Time{}
	for {
		if err := a.sample(ctx); err != nil && ctx.Err() == nil && a.now().Sub(lastLog) >= time.Minute {
			a.log.Warn("节点统计暂不可用", "error", err)
			lastLog = a.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Agent) controlJSON(ctx context.Context, method, path string, body any, desired *model.AgentDesired) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.ControlURL+path, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("控制服务地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.controlClient.Do(req)
	if err != nil {
		return errors.New("无法连接控制服务")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("控制服务返回 HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(desired); err != nil {
		return errors.New("控制服务返回无效配置")
	}
	return nil
}

func (a *Agent) sync(ctx context.Context) error {
	var desired model.AgentDesired
	configErr := a.controlJSON(ctx, http.MethodGet, "/api/v1/agents/config", nil, &desired)
	if configErr == nil {
		configErr = a.applyDesired(desired)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	batch, err := a.ledger.batch()
	if err != nil {
		return err
	}
	report := model.AgentReport{
		At: a.now().UTC(), Daily: batch.daily, Minutes: batch.minutes,
		HistoryCompacted: batch.compacted,
	}
	a.cacheMu.RLock()
	report.Revision = a.cache.AppliedRevision
	a.cacheMu.RUnlock()
	a.liveMu.RLock()
	report.Metrics = a.live.Metrics
	report.CoreHealthy = a.live.Healthy && a.now().Sub(a.live.SampleAt) < 3*time.Second
	if report.CoreHealthy {
		report.Online = make(map[string]int, len(a.live.Online))
		for id, count := range a.live.Online {
			report.Online[id] = count
		}
	}
	a.liveMu.RUnlock()
	if err := a.controlJSON(ctx, http.MethodPost, "/api/v1/agents/report", report, &desired); err != nil {
		return err
	}
	if err := a.ledger.acknowledge(batch); err != nil {
		return err
	}
	if err := a.applyDesired(desired); err != nil {
		return err
	}
	return configErr
}

func (a *Agent) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastLog := time.Time{}
	for {
		if err := a.sync(ctx); err != nil && ctx.Err() == nil && a.now().Sub(lastLog) >= time.Minute {
			a.log.Warn("控制服务同步失败，继续使用本地配置并保存流量", "error", err)
			lastLog = a.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Agent) drainRevocations(ctx context.Context) error {
	a.cacheMu.RLock()
	revision := a.cache.DesiredRevision
	pending := make(map[string]pendingRevocation, len(a.cache.Pending))
	for id, value := range a.cache.Pending {
		pending[id] = value
	}
	a.cacheMu.RUnlock()
	if len(pending) == 0 {
		return nil
	}
	var online map[string]int
	if err := a.statsJSON(ctx, http.MethodGet, "/online", nil, &online); err != nil || online == nil {
		if err != nil {
			return err
		}
		return errors.New("无法确认被撤销设备已离线")
	}
	var kick []string
	now := a.now()
	for id, value := range pending {
		if online[id] != 0 {
			value.ZeroSince = 0
			kick = append(kick, id)
		} else if value.ZeroSince == 0 {
			value.ZeroSince = now.UnixNano()
		}
		pending[id] = value
	}
	if len(kick) > 0 {
		sort.Strings(kick)
		if err := a.statsJSON(ctx, http.MethodPost, "/kick", kick, nil); err != nil {
			return err
		}
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if a.cache.DesiredRevision != revision {
		return nil
	}
	next := cachedState{
		DesiredRevision: a.cache.DesiredRevision, AppliedRevision: a.cache.AppliedRevision,
		Credentials: a.cache.Credentials, Pending: pending,
	}
	for id, value := range next.Pending {
		if value.ZeroSince != 0 && now.Sub(time.Unix(0, value.ZeroSince)) >= a.drainGrace && now.Sub(time.Unix(0, value.Since)) >= a.drainGrace {
			delete(next.Pending, id)
		}
	}
	if len(next.Pending) == 0 {
		next.AppliedRevision = next.DesiredRevision
	}
	if err := a.ledger.saveCache(next); err != nil {
		return err
	}
	a.cache = next
	return nil
}

func (a *Agent) revokeLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastLog := time.Time{}
	for {
		if err := a.drainRevocations(ctx); err != nil && ctx.Err() == nil && a.now().Sub(lastLog) >= time.Minute {
			a.log.Warn("设备撤销待确认，将继续禁止鉴权并重试断开", "error", err)
			lastLog = a.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.revokeWake:
		}
	}
}

func (a *Agent) maintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := a.ledger.maintain(a.now(), maxPendingBytes); err != nil && ctx.Err() == nil {
			a.log.Warn("清理流量历史失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
