package clientbridge

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/client"
)

// A controller capture is paused after taking its immutable copy. Disconnect
// must still execute while the bridge preserves the ordering of that capture.
type capturedController struct {
	Controller
	mu       sync.Mutex
	view     client.View
	block    bool
	captured chan struct{}
	release  chan struct{}
}

func (c *capturedController) View() client.View {
	c.mu.Lock()
	view, block := c.view, c.block
	c.block = false
	c.mu.Unlock()
	if block {
		close(c.captured)
		<-c.release
	}
	return view
}

func (c *capturedController) SelectSubscriptionNode(sub, node string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.view.DesiredSubscriptionID, c.view.DesiredNodeID = sub, node
	return nil
}

func (c *capturedController) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.view.ConnectionIntent, c.view.RunState = false, "disconnected"
	c.view.Metrics.DelayPending = false
	return nil
}

func TestSnapshotSequenceFollowsCaptureAndDoesNotBlockDisconnect(t *testing.T) {
	c := &capturedController{block: true, captured: make(chan struct{}), release: make(chan struct{})}
	c.view.Status = client.Status{RunState: "connected", ConnectionIntent: true}
	c.view.Metrics.DelayPending = true
	bridge := &Bridge{Controller: c}
	var once sync.Once
	release := func() { once.Do(func() { close(c.release) }) }
	defer release()
	oldReply := make(chan string, 1)
	go func() {
		oldReply <- bridge.Call(`{"id":1,"method":"node","params":{"subscriptionId":"a","nodeId":"b"}}`)
	}()
	select {
	case <-c.captured:
	case <-time.After(time.Second):
		t.Fatal("node view was not captured")
	}
	disconnected := make(chan string, 1)
	go func() { disconnected <- bridge.Call(`{"id":2,"method":"disconnect"}`) }()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("snapshot lock blocked Disconnect")
	}
	newReply := make(chan string, 1)
	go func() { newReply <- bridge.Call(`{"id":3,"method":"state"}`) }()
	release()
	decode := func(raw string) Snapshot {
		t.Helper()
		var response struct {
			Result Snapshot
			Error  string
		}
		if err := json.Unmarshal([]byte(raw), &response); err != nil || response.Error != "" {
			t.Fatalf("invalid response: %v %s", err, raw)
		}
		return response.Result
	}
	old, current := decode(<-oldReply), decode(<-newReply)
	if old.SnapshotSequence == 0 || current.SnapshotSequence <= old.SnapshotSequence {
		t.Fatal("snapshot order missing or reversed", old.SnapshotSequence, current.SnapshotSequence)
	}
	if old.RunState != "connected" || !old.Metrics.DelayPending || current.RunState != "disconnected" || current.Metrics.DelayPending || current.DesiredNodeID != "b" {
		t.Fatal("fixture did not preserve the exact old/new state boundary")
	}
}
