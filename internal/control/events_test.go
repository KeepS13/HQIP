package control

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestProgressStreamOutlivesServerWriteTimeout(t *testing.T) {
	s, handler := newServer(t)
	cookie, _ := loginAdmin(t, s, handler)
	n, err := s.Store.CreateNode(model.Node{Name: "stream", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "ssh-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Event(n.ID, "test", "first-progress"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(handler)
	ts.Config.WriteTimeout = 500 * time.Millisecond
	ts.Start()
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/nodes/"+n.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", cookie)
	response, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("stream rejected", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	readProgress := func(message string) {
		t.Helper()
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data:") && strings.Contains(scanner.Text(), message) {
				return
			}
		}
		t.Fatalf("progress %q lost: %v", message, scanner.Err())
	}
	readProgress("first-progress")
	time.Sleep(750 * time.Millisecond)
	if err = s.Store.Event(n.ID, "test", "later-progress"); err != nil {
		t.Fatal(err)
	}
	readProgress("later-progress")
}
