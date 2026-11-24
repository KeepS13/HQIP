package client

import (
	"context"
	"sync"
	"time"
)

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
		c.view.NodeTests.Results[run.resultSlots[resultIndex]] = NodeDelayResult{NodeID: run.nodes[resultIndex].ID, Status: status, DelayMS: delay}
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
	for worker := 0; worker < min(3, len(run.leaders)); worker++ {
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
