package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

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
	a.sampleMu.Lock()
	defer a.sampleMu.Unlock()
	defer a.addDirectLive()
	ctx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	if err := a.sampleDirect(ctx); err != nil {
		a.sampleFailed()
		return err
	}
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
	lastNetwork := time.Time{}
	for {
		if networkSampleDue(a.now(), lastNetwork) {
			lastNetwork = a.now()
			if err := a.sampleHostNetwork(ctx); err != nil && ctx.Err() == nil {
				a.log.Warn("服务器网卡采集失败，保留已有累计", "error", err)
			}
		}
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
