package clientbridge

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
)

// ServePipes accepts only an inherited private pipe, not a network listener.
// It bounds in-flight work and lets the host cancel controller operations before
// waiting for workers on EOF. Replies are serialized, including long logins.
func ServePipes(input io.Reader, output io.Writer, b *Bridge, closeController func()) error {
	var outputMu sync.Mutex
	var workers sync.WaitGroup
	defer workers.Wait()
	defer closeController()
	slots := make(chan struct{}, 16)
	write := func(raw string) { outputMu.Lock(); defer outputMu.Unlock(); _, _ = io.WriteString(output, raw+"\n") }
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 65536)
	for scanner.Scan() {
		raw := scanner.Text()
		select {
		case slots <- struct{}{}:
			workers.Add(1)
			go func() { defer workers.Done(); defer func() { <-slots }(); write(b.Call(raw)) }()
		default:
			var request Request
			_ = json.Unmarshal([]byte(raw), &request)
			response, _ := json.Marshal(Response{ID: request.ID, Error: "操作过于频繁，请稍后重试"})
			write(string(response))
		}
	}
	return scanner.Err()
}
