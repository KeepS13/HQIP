package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

func (c *Core) watchProbeCertificate(ctx context.Context) (func(bool) bool, error) {
	port, secret, err := c.metricsEndpoint()
	if err != nil {
		return nil, err
	}
	config, err := websocket.NewConfig(fmt.Sprintf("ws://127.0.0.1:%d/logs?level=error", port), "http://localhost")
	if err != nil {
		return nil, err
	}
	config.Header.Set("Authorization", "Bearer "+secret)
	connectCtx, cancel := context.WithTimeout(ctx, time.Second)
	conn, err := config.DialContext(connectCtx)
	cancel()
	if err != nil {
		return nil, errors.New("本地代理诊断接口不可用")
	}
	conn.MaxPayloadBytes = 64 << 10
	var certificate atomic.Bool
	verifiedFailure := make(chan struct{}, 1)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	go func() {
		defer close(done)
		for {
			var data []byte
			if websocket.Message.Receive(conn, &data) != nil {
				return
			}
			var entry struct {
				Payload string `json:"payload"`
			}
			if json.Unmarshal(data, &entry) != nil {
				continue
			}
			// The pinned HY2 implementation propagates Go x509 verification errors.
			if strings.Contains(entry.Payload, "certificate") && (strings.Contains(entry.Payload, "x509:") || strings.Contains(entry.Payload, "tls:")) {
				certificate.Store(true)
				select {
				case verifiedFailure <- struct{}{}:
				default:
				}
			}
		}
	}()
	return func(failed bool) bool {
		// The proxy response and log websocket are separate streams. Give a
		// failed CONNECT's already-published diagnostic time to reach us.
		if failed {
			timer := time.NewTimer(150 * time.Millisecond)
			select {
			case <-verifiedFailure:
			case <-done:
			case <-ctx.Done():
			case <-timer.C:
			}
			timer.Stop()
		}
		stop()
		conn.Close()
		<-done
		return certificate.Load()
	}, nil
}
