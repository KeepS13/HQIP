// Package previewhost hosts UI sessions independently of the VPN controller.
package previewhost

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KeepS13/HQIP/internal/clientbridge"
)

type Host struct {
	Bridge     *clientbridge.Bridge
	OpenWindow func()
	UIReady    func()
	Quit       func()
}

// Serve closes only this UI session. Dropping a window must not stop the VPN.
// Slow readers receive at most one latest snapshot; command replies are bounded.
func (h *Host) Serve(parent context.Context, conn net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer conn.Close()
	replies := make(chan []byte, 18)
	events := make(chan []byte, 1)
	var watching atomic.Bool
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); <-ctx.Done(); conn.Close() }()
	go func() {
		defer readers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if watching.Load() {
					snapshot := h.Bridge.Call(`{"id":1,"method":"state"}`)
					var r clientbridge.Response
					if json.Unmarshal([]byte(snapshot), &r) == nil {
						raw, _ := json.Marshal(map[string]any{"event": "state", "result": r.Result})
						select {
						case <-events:
						default:
						}
						select {
						case events <- raw:
						default:
						}
					}
				}
			}
		}
	}()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer cancel()
		for {
			var raw []byte
			select {
			case <-ctx.Done():
				return
			case raw = <-replies:
			case raw = <-events:
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(append(raw, '\n')); err != nil {
				return
			}
		}
	}()
	send := func(raw []byte) {
		select {
		case replies <- raw:
		case <-ctx.Done():
		}
	}
	slots := make(chan struct{}, 16)
	urgent := make(chan struct{}, 2)
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 65536)
	for scanner.Scan() {
		raw := scanner.Text()
		var req clientbridge.Request
		if json.Unmarshal([]byte(raw), &req) != nil || req.ID <= 0 {
			cancel()
			break
		}
		respond := func(result any, message string) {
			b, _ := json.Marshal(clientbridge.Response{ID: req.ID, Result: result, Error: message})
			send(b)
		}
		switch req.Method {
		case "watchState":
			watching.Store(true)
			respond(true, "")
			continue
		case "unwatchState":
			watching.Store(false)
			respond(true, "")
			continue
		case "openWindow":
			respond(true, "")
			if h.OpenWindow != nil {
				h.OpenWindow()
			}
			continue
		case "uiReady":
			respond(true, "")
			if h.UIReady != nil {
				h.UIReady()
			}
			continue
		case "quitApp":
			respond(true, "")
			if h.Quit != nil {
				go h.Quit()
			}
			continue
		}
		pool := slots
		if req.Method == "disconnect" || req.Method == "cancelLogin" {
			pool = urgent
		}
		select {
		case pool <- struct{}{}:
			go func(text string, owned chan struct{}) { defer func() { <-owned }(); send([]byte(h.Bridge.Call(text))) }(raw, pool)
		default:
			respond(nil, "操作过于频繁，请稍后重试")
		}
	}
	cancel()
	<-writerDone
	readers.Wait()
}
