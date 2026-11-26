package previewhost

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/clientbridge"
)

type fakeController struct {
	clientbridge.Controller
	calls   atomic.Int32
	stopped atomic.Int32
	login   chan struct{}
}

func (f *fakeController) View() client.View {
	f.calls.Add(1)
	return client.View{LoggedIn: true, Status: client.Status{RunState: "connected"}}
}
func (f *fakeController) LoginBrowser(string, func(string) error) error { <-f.login; return nil }
func (f *fakeController) Disconnect() error                             { f.stopped.Add(1); return nil }

func TestClosingAndReopeningUIKeepsController(t *testing.T) {
	f := &fakeController{}
	h := &Host{Bridge: &clientbridge.Bridge{Controller: f}}
	for range 3 {
		server, ui := net.Pipe()
		done := make(chan struct{})
		go func() { h.Serve(context.Background(), server); close(done) }()
		_ = ui.SetDeadline(time.Now().Add(time.Second))
		fmt.Fprintln(ui, `{"id":1,"method":"state"}`)
		raw, err := bufio.NewReader(ui).ReadString('\n')
		if err != nil || !strings.Contains(raw, `"connected"`) {
			t.Fatal(raw, err)
		}
		_ = ui.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("session goroutines did not stop")
		}
	}
	if f.stopped.Load() != 0 {
		t.Fatal("window closure disconnected controller")
	}
}

func TestDisconnectBypassesBlockedLogin(t *testing.T) {
	f := &fakeController{login: make(chan struct{})}
	defer close(f.login)
	h := &Host{Bridge: &clientbridge.Bridge{Controller: f}}
	server, ui := net.Pipe()
	defer ui.Close()
	go h.Serve(context.Background(), server)
	_ = ui.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprintln(ui, `{"id":1,"method":"login"}`)
	fmt.Fprintln(ui, `{"id":2,"method":"disconnect"}`)
	raw, err := bufio.NewReader(ui).ReadString('\n')
	if err != nil || !strings.Contains(raw, `"id":2`) || f.stopped.Load() != 1 {
		t.Fatal("disconnect blocked", raw, err)
	}
}
