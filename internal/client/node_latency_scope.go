package client

import (
	"context"
	"errors"
	"slices"
	"time"
)

// TestSelectedNodeDelay accepts the displayed identity, revalidates it under
// mu, and probes exactly one catalog endpoint. It neither authorizes a package
// nor changes a connection. A stale UI choice fails instead of testing another
// node. Duplicate clicks share a job; an explicit new scope replaces the old job.
func (c *Controller) TestSelectedNodeDelay(subscriptionID, nodeID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return context.Canceled
	}
	id, selected := c.view.DesiredSubscriptionID, c.view.DesiredNodeID
	if draft := c.localChoiceLocked(); draft != nil {
		id, selected = draft.SubscriptionID, draft.NodeID
	}
	node, ok := c.catalogTestNodeLocked(subscriptionID, nodeID)
	if c.api == nil || !c.view.LoggedIn || !c.saved.Profile.Enabled || !ok ||
		subscriptionID != id || nodeID != selected || c.view.SelectionPending {
		return errors.New("请先选择有效订阅中可用的节点，再测试当前节点延迟")
	}
	c.invalidateNodeTestLocked()
	if c.view.NodeTests.Pending && c.nodeTest.targetNodeID == nodeID && c.nodeTest.subscriptionID == subscriptionID {
		return nil
	}
	c.launchNodeTestsLocked(&nodeTestRun{subscriptionID: subscriptionID, targetNodeID: nodeID, nodes: []Node{node}})
	return nil
}

// CancelSelectedNodeDelay only owns the requested single-node job. A delayed
// cancellation cannot stop a catalog scan or a different node's newer job.
func (c *Controller) CancelSelectedNodeDelay(subscriptionID, nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if run := c.nodeTest; run != nil && nodeID != "" && run.targetNodeID == nodeID && run.subscriptionID == subscriptionID {
		run.cancel()
		c.finishNodeTestLocked(run)
	}
}

// Caller holds mu. Catalog membership grants handshake probing only; returned
// nodes contain no proxy password. Selection changes do not invalidate a probe
// of its original endpoint, but revoked membership or endpoint changes do.
func (c *Controller) catalogTestNodeLocked(subscriptionID, nodeID string) (Node, bool) {
	for _, sub := range c.saved.Profile.Subscriptions {
		if sub.ID == subscriptionID && sub.QuotaBytes != nil && sub.UsedBytes >= *sub.QuotaBytes {
			return Node{}, false
		}
	}
	choices, nodes := nodeChoices(c.saved.Profile)
	for _, choice := range choices {
		if choice.SubscriptionID != subscriptionID || choice.NodeID != nodeID || !choice.Ready {
			continue
		}
		for _, node := range nodes {
			if node.ID == nodeID {
				return node, true
			}
		}
	}
	return Node{}, false
}

// Caller holds mu and validates eligibility. Only one job owns publication.
// Replacing it cancels outstanding sockets; stale workers cannot publish. A
// single-node refresh retains other eligible endpoints' cached results without
// scheduling them. Close waits for the same bounded worker mechanism.
func (c *Controller) launchNodeTestsLocked(run *nodeTestRun) {
	if c.nodeTest != nil {
		c.nodeTest.cancel()
		c.finishNodeTestLocked(c.nodeTest)
	}
	var previous []NodeDelayResult
	if run.targetNodeID != "" && c.nodeTest != nil && c.nodeTest.api == c.api {
		_, eligible := nodeChoices(c.saved.Profile)
		for _, result := range c.view.NodeTests.Results {
			if slices.ContainsFunc(eligible, func(n Node) bool { return n.ID == result.NodeID }) {
				previous = append(previous, result)
			}
		}
	}
	ctx, cancel := context.WithTimeout(c.workCtx, 90*time.Second)
	run.api, run.generation, run.selection = c.api, c.generation, c.selectionGeneration
	run.mode, run.tun, run.cancel = c.saved.Mode, c.saved.TUN, cancel
	run.groups = map[int][]int{}
	c.nodeTest = run
	c.view.NodeTests = NodeTestView{SubscriptionID: run.subscriptionID, NodeID: run.targetNodeID, Pending: true, Results: previous}
	if run.all {
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
		result := NodeDelayResult{NodeID: node.ID, Status: status}
		slot := slices.IndexFunc(c.view.NodeTests.Results, func(r NodeDelayResult) bool { return r.NodeID == node.ID })
		if slot < 0 {
			slot = len(c.view.NodeTests.Results)
			c.view.NodeTests.Results = append(c.view.NodeTests.Results, result)
		} else {
			c.view.NodeTests.Results[slot] = result
		}
		run.resultSlots = append(run.resultSlots, slot)
	}
	probe := c.nodeProbe
	if probe == nil {
		probe = probeNodeHandshake
	}
	c.workers.Add(1) // Same closed gate/lock as Close; no untracked probe goroutine.
	go func() { defer c.workers.Done(); c.runNodeTests(ctx, run, probe) }()
}
