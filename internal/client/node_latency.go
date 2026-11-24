package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
)

type NodeDelayResult struct {
	NodeID  string `json:"nodeId"`
	Status  string `json:"status"`
	DelayMS int64  `json:"delayMs"`
}
type NodeTestView struct {
	SubscriptionID string `json:"subscriptionId"`
	// NodeID identifies a single-target job; empty retains the catalog/legacy scope.
	NodeID  string            `json:"nodeId,omitempty"`
	Pending bool              `json:"pending"`
	Results []NodeDelayResult `json:"results"`
}
type nodeTestRun struct {
	api                   *API
	generation, selection uint64
	subscriptionID        string
	targetNodeID          string
	nodes                 []Node
	resultSlots           []int
	all                   bool
	mode                  string
	tun                   bool
	groups                map[int][]int
	leaders               []int
	cancel                context.CancelFunc
}

func (c *Controller) nodeTestValidLocked(run *nodeTestRun) bool {
	p := c.saved.Profile
	if run.targetNodeID != "" {
		node, ok := c.catalogTestNodeLocked(run.subscriptionID, run.targetNodeID)
		return !c.closed && c.api == run.api && c.view.LoggedIn && p.Enabled &&
			c.saved.Mode == run.mode && c.saved.TUN == run.tun && ok && node == run.nodes[0]
	}
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

// Reached only by an explicit manual test command. Login, Sync, selection and
// connection lifecycle never start a handshake scan or consume probe traffic.
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
	if c.view.NodeTests.Pending && c.nodeTest.targetNodeID == "" {
		return nil
	}
	c.launchNodeTestsLocked(&nodeTestRun{subscriptionID: p.SubscriptionID, nodes: nodes, all: all})
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
