package client

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// /traffic is a streaming global counter that includes direct traffic. The
// dashboard uses only proxy counters, so a stalled stream must be irrelevant.
func TestCoreSampleSkipsUnusedTrafficStream(t *testing.T) {
	var connections, stats, traffic atomic.Int32
	core := metricCore(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connections":
			connections.Add(1)
			_, _ = w.Write([]byte(`{"memory":2048,"connections":[{}]}`))
		case "/traffic":
			traffic.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected metrics endpoint %s", r.URL.Path)
		}
	})
	srv := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.Add(1)
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		_, _ = w.Write([]byte{0, 0, 0, 0, 0})
		w.Header().Set("Grpc-Status", "0")
	}), &http2.Server{}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	core.statsPort, _ = strconv.Atoi(port)
	sample, err := core.Sample(context.Background())
	if err != nil || !sample.ProxyOnly || sample.Connections != 1 || sample.Memory != 2048 {
		t.Fatalf("unused global stream prevented proxy sample: %+v %v", sample, err)
	}
	if traffic.Load() != 0 || connections.Load() != 1 || stats.Load() != 1 {
		t.Fatalf("unnecessary samples: traffic=%d connections=%d stats=%d", traffic.Load(), connections.Load(), stats.Load())
	}
}
