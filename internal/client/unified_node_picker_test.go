package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func catalogProfile() Profile {
	p := Profile{Enabled: true, AvailableNodes: []Node{{ID: "shared", Name: "Tokyo", Server: "203.0.113.10", Port: 443, SNI: "node.example", Password: "must-not-escape", Ready: true}, {ID: "alias", Name: "Tokyo alias", Server: "203.0.113.10", Port: 443, SNI: "NODE.EXAMPLE.", Ready: true}}}
	for _, id := range []string{"first", "second", "third"} {
		node := "shared"
		if id == "third" {
			node = "alias"
		}
		p.Subscriptions = append(p.Subscriptions, Subscription{ID: id, TemplateName: "Same plan", Status: "active", ExpiresAt: time.Now().Add(time.Hour), NodeIDs: []string{node}})
	}
	return p
}

func TestNodeChoicesKeepSubscriptionIdentityAndHideInvalid(t *testing.T) {
	p := catalogProfile()
	for _, status := range []string{"expired", "exhausted", "disabled", "removed"} {
		p.Subscriptions = append(p.Subscriptions, Subscription{ID: status, Status: status, ExpiresAt: time.Now().Add(time.Hour), NodeIDs: []string{"shared"}})
	}
	p.Subscriptions = append(p.Subscriptions, Subscription{ID: "old-active", Status: "active", ExpiresAt: time.Now().Add(-time.Second), NodeIDs: []string{"shared"}})
	choices, nodes := nodeChoices(p)
	if len(choices) != 3 || len(nodes) != 2 || choices[0].SubscriptionID == choices[1].SubscriptionID || choices[0].NodeID != choices[1].NodeID {
		t.Fatalf("subscription identities lost: %+v %+v", choices, nodes)
	}
	for _, node := range nodes {
		if node.Password != "" {
			t.Fatal("catalog retained a proxy credential")
		}
	}
}

func TestGlobalNodeDelayDeduplicatesEndpointsWithoutChangingSelection(t *testing.T) {
	c, engine, _ := setupClient(t)
	c.mu.Lock()
	c.saved.Profile = catalogProfile()
	c.populateLocked(c.saved.Profile)
	before := c.view.Status
	c.mu.Unlock()
	var calls atomic.Int32
	c.nodeProbe = func(context.Context, Node) (int64, error) { calls.Add(1); return 28, nil }
	if err := c.TestNodeDelays(); err != nil {
		t.Fatal(err)
	}
	v := waitNodeTests(t, c)
	if calls.Load() != 1 || len(v.Results) != 2 || v.SubscriptionID != "" {
		t.Fatalf("repeated physical endpoint: calls=%d results=%+v", calls.Load(), v)
	}
	for _, result := range v.Results {
		if result.DelayMS != 28 || result.Status != "success" {
			t.Fatal("result did not fan out", result)
		}
	}
	if c.View().Status != before || len(engine.calls) != 0 {
		t.Fatal("probe changed connection intent or selected subscription")
	}
	raw, _ := json.Marshal(c.View())
	if strings.Contains(string(raw), "must-not-escape") || strings.Contains(string(raw), "203.0.113.10") {
		t.Fatal("safe UI view exposed endpoint or credential")
	}
}

func TestAutomaticNodeDelaysWaitForSyncAndRunOncePerLogin(t *testing.T) {
	c, _, _ := setupClient(t)
	c.mu.Lock()
	c.saved.Profile = catalogProfile()
	c.view.LastSync = nil
	c.mu.Unlock()
	var calls atomic.Int32
	c.nodeProbe = func(context.Context, Node) (int64, error) { calls.Add(1); return 15, nil }
	c.startAutomaticNodeTests()
	if calls.Load() != 0 {
		t.Fatal("startup probe ran before profile sync")
	}
	now := time.Now()
	c.mu.Lock()
	c.view.LastSync = &now
	c.mu.Unlock()
	for i := 0; i < 5; i++ {
		c.startAutomaticNodeTests()
		waitNodeTests(t, c)
	}
	if calls.Load() != 1 {
		t.Fatal("polling retriggered automatic probing", calls.Load())
	}
	c.mu.Lock()
	api, err := NewAPI(c.saved.ServerURL, c.api.Tokens())
	if err != nil {
		t.Fatal(err)
	}
	c.api = api
	c.mu.Unlock()
	c.startAutomaticNodeTests()
	waitNodeTests(t, c)
	if calls.Load() != 2 {
		t.Fatal("new login did not get its own probe", calls.Load())
	}
}

func TestGlobalNodeTestsKeepSelectionIndependentAndCancelRemovedAccess(t *testing.T) {
	for _, removeAccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "selection-change", true: "removed-access"}[removeAccess], func(t *testing.T) {
			c, _, _ := setupClient(t)
			c.mu.Lock()
			c.saved.Profile = catalogProfile()
			c.mu.Unlock()
			started, release := make(chan struct{}), make(chan struct{})
			c.nodeProbe = func(ctx context.Context, _ Node) (int64, error) {
				close(started)
				select {
				case <-ctx.Done():
					return 0, ctx.Err()
				case <-release:
					return 18, nil
				}
			}
			if err := c.TestNodeDelays(); err != nil {
				t.Fatal(err)
			}
			<-started
			c.mu.Lock()
			c.generation++
			c.selectionGeneration++
			c.saved.SelectedSubscriptionID = "second"
			c.saved.Profile.SubscriptionID = "second"
			if removeAccess {
				for i := range c.saved.Profile.Subscriptions {
					c.saved.Profile.Subscriptions[i].Status = "removed"
				}
			}
			c.mu.Unlock()
			// Reading a view also checks whether an in-flight probe still belongs
			// to the current account's usable endpoint catalog.
			view := c.View()
			close(release)
			result := waitNodeTests(t, c)
			if removeAccess {
				if view.NodeTests.Pending || len(result.Results) != 0 {
					t.Fatal("access removal retained results", result)
				}
			} else if len(result.Results) != 2 || result.Results[0].Status != "success" {
				t.Fatal("selection change cancelled independent handshake probes", result)
			}
		})
	}
}

type pickerCatalogTransport struct{ next http.RoundTripper }

func (tr pickerCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := tr.next.RoundTrip(r)
	if err != nil || response.StatusCode != http.StatusOK || (r.URL.Path != "/api/v3/client/profile" && r.URL.Path != "/api/v3/client/subscription") {
		return response, err
	}
	var p Profile
	err = json.NewDecoder(response.Body).Decode(&p)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	p.AvailableNodes = []Node{{ID: "same-node", Name: "Default", Ready: true}, {ID: "alternate", Name: "Alternate", Ready: true}}
	for i := range p.Subscriptions {
		if p.Subscriptions[i].ID == "b" {
			p.Subscriptions[i].NodeIDs = append(p.Subscriptions[i].NodeIDs, "alternate")
		}
	}
	if p.SubscriptionID == "b" {
		p.Nodes = append(p.Nodes, Node{ID: "alternate", Name: "Alternate", Password: "credential-b-alternate", Ready: true})
	}
	raw, _ := json.Marshal(p)
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	return response, nil
}

func TestCombinedNodeSelectionConnectsOnlyRequestedSubscriptionAndNode(t *testing.T) {
	c, engine, _ := subscriptionClient(t)
	c.api.http.Transport = pickerCatalogTransport{next: c.api.http.Transport}
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "connected")
	if err := c.SelectSubscriptionNode("b", "alternate"); err != nil {
		t.Fatal(err)
	}
	waitSubscription(t, c, "b")
	waitState(t, c, "connected")
	v := c.View()
	if v.DesiredSubscriptionID != "b" || v.ActualSubscriptionID != "b" || v.DesiredNodeID != "alternate" || v.ActualNodeID != "alternate" {
		t.Fatal("combined selection lost identity", v.Status)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.calls) != 2 || engine.calls[0] != "same-node" || engine.calls[1] != "alternate" {
		t.Fatal("connected an intermediate default node", engine.calls)
	}
}

func TestCombinedNodeSelectionRejectsOtherSubscriptionNode(t *testing.T) {
	c, _, _ := subscriptionClient(t)
	c.api.http.Transport = pickerCatalogTransport{next: c.api.http.Transport}
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := c.View().Status
	if err := c.SelectSubscriptionNode("a", "alternate"); err == nil {
		t.Fatal("accepted node from another subscription")
	}
	if c.View().Status != before {
		t.Fatal("rejected choice changed state")
	}
}
