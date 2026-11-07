package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"sync"
	"time"
)

type NodeDelayResult struct {
	NodeID  string `json:"nodeId"`
	Status  string `json:"status"`
	DelayMS int64  `json:"delayMs"`
}
type NodeTestView struct {
	SubscriptionID string            `json:"subscriptionId"`
	Pending        bool              `json:"pending"`
	Results        []NodeDelayResult `json:"results"`
}
type nodeTestRun struct {
	api                   *API
	generation, selection uint64
	subscriptionID        string
	nodes                 []Node
	cancel                context.CancelFunc
}

func (c *Controller) nodeTestValidLocked(run *nodeTestRun) bool {
	p := c.saved.Profile
	return !c.closed && c.api == run.api && c.view.LoggedIn && !c.view.SelectionPending &&
		c.generation == run.generation && c.selectionGeneration == run.selection && p.Enabled && usableSubscription(p) &&
		c.saved.SelectedSubscriptionID == run.subscriptionID && p.SubscriptionID == run.subscriptionID && slices.Equal(p.Nodes, run.nodes)
}

func (c *Controller) invalidateNodeTestLocked() {
	if c.nodeTest != nil && !c.nodeTestValidLocked(c.nodeTest) {
		c.nodeTest.cancel()
		c.nodeTest = nil
		c.view.NodeTests = NodeTestView{}
	}
}

// TestNodeDelays measures only authenticated TLS handshakes. It never opens a
// proxy stream, acquires a VPN lease, or changes the active core/selection.
func (c *Controller) TestNodeDelays() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateNodeTestLocked()
	p := c.saved.Profile
	if c.closed {
		return context.Canceled
	}
	if c.api == nil || !c.view.LoggedIn || c.view.SelectionPending || !p.Enabled || !usableSubscription(p) || c.saved.SelectedSubscriptionID != p.SubscriptionID {
		return errors.New("请先选择有效订阅，再测试节点延迟")
	}
	if len(p.Nodes) == 0 {
		return errors.New("当前订阅暂无可测试节点")
	}
	if c.view.NodeTests.Pending {
		return nil
	}
	if c.nodeTest != nil {
		c.nodeTest.cancel()
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 90*time.Second)
	run := &nodeTestRun{api: c.api, generation: c.generation, selection: c.selectionGeneration, subscriptionID: p.SubscriptionID, nodes: slices.Clone(p.Nodes), cancel: cancel}
	c.nodeTest = run
	c.view.NodeTests = NodeTestView{SubscriptionID: p.SubscriptionID, Pending: true}
	for _, node := range run.nodes {
		status := "waiting"
		if !node.Ready {
			status = "unavailable"
		}
		c.view.NodeTests.Results = append(c.view.NodeTests.Results, NodeDelayResult{NodeID: node.ID, Status: status})
	}
	probe := c.nodeProbe
	if probe == nil {
		probe = probeNodeHandshake
	}
	c.workers.Add(1) // Protected by the same closed gate as Close.
	go func() { defer c.workers.Done(); c.runNodeTests(ctx, run, probe) }()
	return nil
}

func (c *Controller) CancelNodeDelays() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nodeTest != nil {
		c.nodeTest.cancel()
	}
	c.finishNodeTestLocked(c.nodeTest)
}

func (c *Controller) finishNodeTestLocked(run *nodeTestRun) {
	if run == nil || c.nodeTest != run {
		return
	}
	for i := range c.view.NodeTests.Results {
		result := &c.view.NodeTests.Results[i]
		if result.Status == "waiting" || result.Status == "testing" {
			result.Status = "cancelled"
		}
	}
	c.view.NodeTests.Pending = false
}

func (c *Controller) publishNodeDelay(run *nodeTestRun, index int, status string, delay int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateNodeTestLocked()
	if c.nodeTest != run || !c.view.NodeTests.Pending {
		return false
	}
	c.view.NodeTests.Results[index] = NodeDelayResult{NodeID: run.nodes[index].ID, Status: status, DelayMS: delay}
	return true
}

func (c *Controller) runNodeTests(ctx context.Context, run *nodeTestRun, probe func(context.Context, Node) (int64, error)) {
	defer run.cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.mu.Lock()
				c.invalidateNodeTestLocked()
				c.mu.Unlock()
			}
		}
	}()
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				if !c.publishNodeDelay(run, index, "testing", 0) {
					return
				}
				attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
				delay, err := probe(attempt, run.nodes[index])
				cancel()
				if ctx.Err() != nil {
					return
				}
				c.publishNodeDelay(run, index, nodeProbeStatus(err), delay)
			}
		}()
	}
dispatch:
	for index, node := range run.nodes {
		if !node.Ready {
			continue
		}
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	run.cancel()
	<-done
	c.mu.Lock()
	c.invalidateNodeTestLocked()
	c.finishNodeTestLocked(run)
	c.mu.Unlock()
}

func nodeProbeStatus(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, errProbeDirect) {
		return "direct_unavailable"
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "certificate_error"
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "failed"
}
