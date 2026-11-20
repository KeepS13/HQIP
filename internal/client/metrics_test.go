package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func metricCore(t *testing.T, handler http.HandlerFunc) *Core {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return &Core{Platform: fakePlatform{}, Dir: t.TempDir(), port: 1, apiPort: number, secret: "test-secret"}
}

func TestCoreMetricsSummarizeOnlyAndUseAuthenticatedLoopback(t *testing.T) {
	core := metricCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing metrics authentication")
		}
		switch r.URL.Path {
		case "/traffic":
			_, _ = w.Write([]byte(`{"up":100,"down":900}`))
		case "/connections":
			_, _ = w.Write([]byte(`{"uploadTotal":1234,"downloadTotal":4567,"memory":2048,"connections":[{"metadata":{"host":"not-exposed.example"}},{"id":"two"}]}`))
		case "/proxies/proxy/delay":
			if r.URL.Query().Get("url") != "https://www.cloudflare.com/cdn-cgi/trace" {
				t.Error("unexpected probe target")
			}
			_, _ = w.Write([]byte(`{"delay":28}`))
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	})
	s, err := core.Sample(context.Background())
	if err != nil || s.UploadRate != 100 || s.DownloadRate != 900 || s.UploadTotal != 1234 || s.DownloadTotal != 4567 || s.Connections != 2 || s.Memory != 2048 {
		t.Fatalf("bad sample %+v %v", s, err)
	}
	delay, err := core.Delay(context.Background())
	if err != nil || delay != 28 {
		t.Fatalf("bad delay %d %v", delay, err)
	}
}

func TestSlowCoreMetricsCannotBlockStop(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	core := metricCore(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(arrived) })
		<-release
		_, _ = w.Write([]byte(`{}`))
	})
	done := make(chan error, 1)
	go func() { _, err := core.Sample(context.Background()); done <- err }()
	<-arrived
	stopped := make(chan error, 1)
	go func() { stopped <- core.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("metrics HTTP held core lock")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("stopped generation returned valid sample")
	}
}

type sampleEngine struct {
	fakeEngine
	sample  CoreSample
	err     error
	gate    <-chan struct{}
	started chan<- struct{}
}

func (e *sampleEngine) Sample(ctx context.Context) (CoreSample, error) {
	if e.started != nil {
		e.started <- struct{}{}
	}
	if e.gate != nil {
		select {
		case <-e.gate:
		case <-ctx.Done():
			return CoreSample{}, ctx.Err()
		}
	}
	return e.sample, e.err
}

func TestDashboardHistoryIsBoundedAndFailureIsNotZero(t *testing.T) {
	e := &sampleEngine{sample: CoreSample{UploadRate: 100, DownloadRate: 200, UploadTotal: 1000, DownloadTotal: 2000, Connections: 3}}
	c, err := New(t.TempDir(), fakePlatform{}, e)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.view.RunState = "connected"
	c.beginMetricsLocked()
	for i := 0; i < 80; i++ {
		c.sampleMetrics(context.Background())
	}
	m := c.View().Metrics
	if len(m.History) != 60 || !m.Available || m.Stale || m.UploadBytes != 1000 || m.DownloadBytes != 2000 {
		t.Fatalf("bad dashboard %+v", m)
	}
	e.err = errors.New("temporarily unavailable")
	c.sampleMetrics(context.Background())
	m = c.View().Metrics
	if !m.Stale || m.DownloadBytes != 2000 {
		t.Fatal("unavailable sample erased valid counters")
	}
	c.view.RunState = "disconnected"
	c.sampleMetrics(context.Background())
	m = c.View().Metrics
	if m.Available || m.DownloadRate != 0 || m.UploadRate != 0 {
		t.Fatal("disconnected view showed stale rate")
	}
}

func TestLateMetricsCannotReplaceNewConnection(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	e := &sampleEngine{sample: CoreSample{UploadTotal: 999}, gate: release, started: started}
	c, err := New(t.TempDir(), fakePlatform{}, e)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.view.RunState = "connected"
	c.beginMetricsLocked()
	done := make(chan struct{})
	go func() { c.sampleMetrics(context.Background()); close(done) }()
	<-started
	c.mu.Lock()
	c.generation++
	c.beginMetricsLocked()
	c.mu.Unlock()
	close(release)
	<-done
	if c.View().Metrics.UploadBytes != 0 {
		t.Fatal("previous connection sample crossed billing identity")
	}
}

func TestAccountURLUsesUserFrontend(t *testing.T) {
	for _, address := range []string{DefaultServerURL, DefaultServerURL + "/", previousOfficialServerURL, previousOfficialServerURL + "/"} {
		if AccountURL(address) != "https://hqip.sharevps.online/subscriptions" {
			t.Fatal("account link used API host or old user domain")
		}
	}
	for _, address := range []string{"https://custom.example", "https://api-hqip.sharevps.online.evil.example", "https://api-hqip.sharevps.online:444", legacyServerURL, legacyUserSiteURL} {
		for _, suffix := range []string{"", "/"} {
			if AccountURL(address+suffix) != address+"/subscriptions" {
				t.Fatal("custom account origin changed", address)
			}
		}
	}
}
