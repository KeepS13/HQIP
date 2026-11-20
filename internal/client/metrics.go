package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type MetricPoint struct {
	At           time.Time `json:"at"`
	UploadRate   int64     `json:"uploadRate"`
	DownloadRate int64     `json:"downloadRate"`
}

type Dashboard struct {
	BillingEstimated       bool          `json:"billingEstimated"`
	Available              bool          `json:"available"`
	Stale                  bool          `json:"stale"`
	UploadRate             int64         `json:"uploadRate"`
	DownloadRate           int64         `json:"downloadRate"`
	UploadBytes            int64         `json:"uploadBytes"`
	DownloadBytes          int64         `json:"downloadBytes"`
	ApplicationConnections int           `json:"applicationConnections"`
	MemoryBytes            int64         `json:"memoryBytes"`
	ConnectedAt            *time.Time    `json:"connectedAt"`
	UpdatedAt              *time.Time    `json:"updatedAt"`
	DelayMS                *int64        `json:"delayMs"`
	DelayAt                *time.Time    `json:"delayAt"`
	DelayPending           bool          `json:"delayPending"`
	History                []MetricPoint `json:"history"`
}

type CoreSample struct {
	ProxyOnly     bool
	UploadRate    int64
	DownloadRate  int64
	UploadTotal   int64
	DownloadTotal int64
	Connections   int
	Memory        int64
}

type metricsEngine interface {
	Sample(context.Context) (CoreSample, error)
}
type delayEngine interface {
	Delay(context.Context) (int64, error)
}

func (c *Core) metricsEndpoint() (int, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.port == 0 {
		return 0, "", errors.New("内核未连接")
	}
	return c.apiPort, c.secret, nil
}

func readCoreJSON(ctx context.Context, port int, secret, resource string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, resource), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("内核指标暂不可用")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("内核指标暂不可用")
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target); err != nil {
		return errors.New("内核指标响应无效")
	}
	return nil
}

// Never hold Core.mu while reading the HTTP endpoints: Stop must remain able
// to restore network settings even if the core's metrics listener stalls.
func (c *Core) Sample(ctx context.Context) (CoreSample, error) {
	port, secret, err := c.metricsEndpoint()
	if err != nil {
		return CoreSample{}, err
	}
	var traffic struct {
		Up   int64 `json:"up"`
		Down int64 `json:"down"`
	}
	var snapshot struct {
		Upload      int64             `json:"uploadTotal"`
		Download    int64             `json:"downloadTotal"`
		Memory      int64             `json:"memory"`
		Connections []json.RawMessage `json:"connections"`
	}
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- readCoreJSON(requestCtx, port, secret, "/traffic", &traffic) }()
	err = readCoreJSON(requestCtx, port, secret, "/connections", &snapshot)
	if err != nil {
		cancel()
	}
	trafficErr := <-result
	if err == nil {
		err = trafficErr
	}
	if err != nil {
		return CoreSample{}, err
	}
	newPort, newSecret, err := c.metricsEndpoint()
	if err != nil || newPort != port || newSecret != secret {
		return CoreSample{}, errors.New("内核已切换")
	}
	if snapshot.Upload < 0 || snapshot.Download < 0 || traffic.Up < 0 || traffic.Down < 0 {
		return CoreSample{}, errors.New("内核指标响应无效")
	}
	c.mu.Lock()
	statsPort := c.statsPort
	c.mu.Unlock()
	if statsPort > 0 {
		upload, download, err := readProxyStats(requestCtx, statsPort)
		if err != nil {
			return CoreSample{}, err
		}
		newPort, newSecret, err = c.metricsEndpoint()
		if err != nil || newPort != port || newSecret != secret {
			return CoreSample{}, errors.New("内核已切换")
		}
		return CoreSample{ProxyOnly: true, UploadTotal: upload, DownloadTotal: download, Connections: len(snapshot.Connections), Memory: max(0, snapshot.Memory)}, nil
	}
	return CoreSample{}, errors.New("内核缺少代理出口计费统计能力")
}

func (c *Core) Delay(ctx context.Context) (int64, error) {
	port, secret, err := c.metricsEndpoint()
	if err != nil {
		return 0, err
	}
	var result struct {
		Delay int64 `json:"delay"`
	}
	err = readCoreJSON(ctx, port, secret, "/proxies/proxy/delay?timeout=3000&url=https%3A%2F%2Fwww.cloudflare.com%2Fcdn-cgi%2Ftrace", &result)
	if err != nil {
		return 0, errors.New("当前节点延迟测试失败")
	}
	newPort, newSecret, err := c.metricsEndpoint()
	if err != nil || newPort != port || newSecret != secret || result.Delay <= 0 {
		return 0, errors.New("当前节点已切换，请重新测试")
	}
	return result.Delay, nil
}

func (c *Controller) beginMetricsLocked() {
	now := time.Now()
	c.view.Metrics = Dashboard{ConnectedAt: &now, History: []MetricPoint{}}
	c.billingMetrics = billingMetrics{}
}

func (c *Controller) runMetrics(ctx context.Context) {
	if _, ok := c.engine.(metricsEngine); !ok {
		return
	}
	billingDone := make(chan struct{})
	go func() { defer close(billingDone); c.runBillingSync(ctx) }()
	defer func() { <-billingDone }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sampleMetrics(ctx)
		}
	}
}

func (c *Controller) sampleMetrics(ctx context.Context) {
	engine, ok := c.engine.(metricsEngine)
	if !ok {
		return
	}
	c.mu.Lock()
	g := c.generation
	connected := connectionRunning(c.view.RunState) && !c.view.SelectionPending
	if !connected {
		c.view.Metrics.Available = false
		c.view.Metrics.UploadRate = 0
		c.view.Metrics.DownloadRate = 0
	}
	c.mu.Unlock()
	if !connected {
		return
	}
	sample, err := engine.Sample(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if g != c.generation || !connectionRunning(c.view.RunState) || c.view.SelectionPending {
		return
	}
	if err != nil {
		c.view.Metrics.Stale = true
		c.view.Metrics.UploadRate, c.view.Metrics.DownloadRate = 0, 0
		c.view.Metrics.ApplicationConnections = 0
		return
	}
	if sample.ProxyOnly {
		c.applyBillingSample(sample, time.Now())
		return
	}
	c.view.Metrics.Available = false
	c.view.Metrics.Stale = true
	c.view.Metrics.UploadRate, c.view.Metrics.DownloadRate = 0, 0
}

func (c *Controller) MeasureDelay() error {
	if !c.beginWork() {
		return context.Canceled
	}
	defer c.workers.Done()
	engine, ok := c.engine.(delayEngine)
	if !ok {
		return errors.New("当前内核不支持延迟检测")
	}
	c.mu.Lock()
	if c.view.RunState != "connected" || c.view.SelectionPending || c.view.Metrics.DelayPending {
		c.mu.Unlock()
		return errors.New("请连接当前节点后再测试延迟")
	}
	g := c.generation
	c.view.Metrics.DelayPending = true
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.workCtx, 5*time.Second)
	defer cancel()
	delay, err := engine.Delay(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if g != c.generation || c.view.RunState != "connected" {
		return nil
	}
	c.view.Metrics.DelayPending = false
	if err != nil {
		c.view.Metrics.DelayMS = nil
		return err
	}
	now := time.Now()
	c.view.Metrics.DelayMS, c.view.Metrics.DelayAt = &delay, &now
	return nil
}
