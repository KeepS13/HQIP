package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
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
	all                   bool
	mode                  string
	tun                   bool
	groups                map[int][]int
	leaders               []int
	cancel                context.CancelFunc
}

func (c *Controller) nodeTestValidLocked(run *nodeTestRun) bool {
	p := c.saved.Profile
	if run.all {
		_, nodes := nodeChoices(p)
		return !c.closed && c.api == run.api && c.view.LoggedIn && p.Enabled &&
			c.saved.Mode == run.mode && c.saved.TUN == run.tun && slices.Equal(nodes, run.nodes)
	}
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
	return c.startNodeTestsLocked()
}

// Each authenticated account is tested once per application lifetime after its
// first successful sync. Refreshes and reopening the picker do not retrigger it.
func (c *Controller) startAutomaticNodeTests() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api == nil || c.autoNodeTestAPI == c.api || c.view.LastSync == nil || c.view.SelectionPending {
		return
	}
	if c.startNodeTestsLocked() == nil {
		c.autoNodeTestAPI = c.api
	}
}

func (c *Controller) startNodeTestsLocked() error {
	c.invalidateNodeTestLocked()
	p := c.saved.Profile
	if c.closed {
		return context.Canceled
	}
	all := p.AvailableNodes != nil
	nodes := slices.Clone(p.Nodes)
	if all {
		_, nodes = nodeChoices(p)
	}
	if c.api == nil || !c.view.LoggedIn || c.view.SelectionPending || !p.Enabled || (!all && (!usableSubscription(p) || c.saved.SelectedSubscriptionID != p.SubscriptionID)) {
		return errors.New("请先登录并同步有效订阅，再测试节点延迟")
	}
	if len(nodes) == 0 {
		return errors.New("有效订阅暂无可测试节点")
	}
	if c.view.NodeTests.Pending {
		return nil
	}
	if c.nodeTest != nil {
		c.nodeTest.cancel()
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 90*time.Second)
	run := &nodeTestRun{api: c.api, generation: c.generation, selection: c.selectionGeneration, subscriptionID: p.SubscriptionID, nodes: nodes, all: all, mode: c.saved.Mode, tun: c.saved.TUN, groups: map[int][]int{}, cancel: cancel}
	c.nodeTest = run
	c.view.NodeTests = NodeTestView{SubscriptionID: p.SubscriptionID, Pending: true}
	if all {
		c.view.NodeTests.SubscriptionID = ""
	}
	endpoints := map[string]int{}
	for index, node := range run.nodes {
		status := "waiting"
		if !node.Ready {
			status = "unavailable"
		} else {
			key := nodeProbeKey(node)
			leader, exists := endpoints[key]
			if !exists {
				leader = index
				endpoints[key] = leader
				run.leaders = append(run.leaders, leader)
			}
			run.groups[leader] = append(run.groups[leader], index)
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
	for _, resultIndex := range run.groups[index] {
		c.view.NodeTests.Results[resultIndex] = NodeDelayResult{NodeID: run.nodes[resultIndex].ID, Status: status, DelayMS: delay}
	}
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
	for _, index := range run.leaders {
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

func nodeProbeKey(node Node) string {
	if node.Server == "" || node.Port == 0 || node.SNI == "" {
		return "id:" + node.ID
	}
	server := strings.ToLower(node.Server)
	if ip := net.ParseIP(server); ip != nil {
		server = ip.String()
	}
	return server + "\n" + strconv.Itoa(node.Port) + "\n" + strings.TrimSuffix(strings.ToLower(node.SNI), ".") + "\n" + node.Protocol + "\n" + node.Reality.PublicKey + "\n" + node.Reality.ShortID
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
