package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/reality"
	"io"
	"math/rand/v2"
	"net/http"
	"time"
)

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
	queueErr := a.replayLeaseFinals(ctx)
	if err := a.syncOfflineGrants(ctx); err != nil {
		queueErr = err
	}
	var desired model.AgentDesired
	configErr := a.controlJSON(ctx, http.MethodGet, "/api/v1/agents/config", nil, &desired)
	if configErr == nil {
		configErr = a.applyDesired(desired)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := a.progressRetirement(ctx); err != nil {
		configErr = err
		a.cacheMu.Lock()
		next := cloneCache(a.cache)
		next.Warning = "节点停止或清理待重试：" + err.Error()
		if saveErr := a.ledger.saveCache(next); saveErr == nil {
			a.cache = next
		}
		a.cacheMu.Unlock()
	}
	if a.now().Before(a.reportRetryAt) {
		return errors.New("流量补报等待重试，本地计数继续保留")
	}
	batch, err := a.ledger.batch()
	if err != nil {
		return err
	}
	report := model.AgentReport{
		At: a.now().UTC(), Daily: batch.daily, Minutes: batch.minutes,
		Hours: batch.hours, HourlySince: batch.hourlySince, HourlyThrough: batch.hourlyThrough,
		HistoryCompacted: batch.compacted,
		Capabilities:     []string{"agent-v2", "hourly-usage-v1", "offline-admission-v1"},
	}
	if a.cfg.Managed {
		report.Capabilities = append(report.Capabilities, "multi-ip-v2", "retirement-v1", "certificate-v1")
	}
	var coreCapabilities struct {
		Admission             bool `json:"admission"`
		Version               int  `json:"version"`
		TrafficBillingVersion int  `json:"trafficBillingVersion"`
	}
	if a.statsJSON(ctx, http.MethodGet, "/hqip/capabilities", nil, &coreCapabilities) == nil && coreCapabilities.Admission && coreCapabilities.Version == 1 {
		report.Capabilities = append(report.Capabilities, "admission-v1")
		if coreCapabilities.TrafficBillingVersion == 1 {
			report.Capabilities = append(report.Capabilities, "traffic-billing-v1")
			report.Capabilities = append(report.Capabilities, model.NodeQuotaCapability)
		}
		if endpoint := a.realityReport(); endpoint != nil {
			report.Reality = endpoint
			report.Capabilities = append(report.Capabilities, model.RealityCapability)
			if reality.VisionEnabled {
				report.Capabilities = append(report.Capabilities, model.RealityVisionCapability)
			}
		}
	}
	a.cacheMu.RLock()
	report.Revision = a.cache.AppliedRevision
	report.Removals = removalStatuses(a.cache)
	if v := a.cache.Retirement; v != nil {
		report.Retirement = &model.RetirementStatus{ID: v.ID, Stopped: v.Stopped, Final: v.Final}
	}
	if v := a.cache.Certificate; v != nil {
		copy := *v
		report.Certificate = &copy
	}
	if a.cache.Warning != "" {
		report.Warnings = []string{a.cache.Warning}
	}
	a.cacheMu.RUnlock()
	if warning := a.ledger.leaseQueueWarning(); warning != "" {
		report.Warnings = append(report.Warnings, warning)
	}
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
		a.deferReportRetry()
		return err
	}
	if err := a.ledger.acknowledge(batch); err != nil {
		a.deferReportRetry()
		return err
	}
	a.reportBackoff, a.reportRetryAt = 0, time.Time{}
	if err := a.applyDesired(desired); err != nil {
		return err
	}
	if err := a.acknowledgeRetirement(ctx, report.Retirement); err != nil {
		return err
	}
	if configErr != nil {
		return configErr
	}
	return queueErr
}

func (a *Agent) deferReportRetry() {
	a.reportBackoff = min(30*time.Second, max(5*time.Second, a.reportBackoff*2))
	// Spread reconnecting nodes over 80%-100% of the capped delay.
	delay := a.reportBackoff*4/5 + time.Duration(rand.Int64N(int64(a.reportBackoff/5)+1))
	a.reportRetryAt = a.now().Add(delay)
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
