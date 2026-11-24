package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

type Controller struct {
	billingMetrics      billingMetrics
	mu                  sync.Mutex
	syncMu              sync.Mutex
	applyMu             sync.Mutex
	routeMu             sync.Mutex
	reportMu            sync.Mutex
	logMu               sync.Mutex
	recoveryProbeMu     sync.Mutex
	recovery            *recoveryRun
	recoveryReady       bool
	suspended           bool
	now                 func() time.Time
	workers             sync.WaitGroup
	closeOnce           sync.Once
	workCtx             context.Context
	cancelWork          context.CancelFunc
	dir                 string
	platform            Platform
	engine              Engine
	api                 *API
	saved               persisted
	bundle              rules.Bundle
	view                View
	etag                string
	generation          uint64
	appliedGeneration   uint64
	selectionGeneration uint64
	cancelApply         context.CancelFunc
	closed              bool
	wake                chan struct{}
	authCancel          context.CancelFunc
	loginGeneration     uint64
	cancelSelection     context.CancelFunc
	selectionUncertain  *API // A canceled selection may have committed remotely; reauthorize this session before reuse.
	cancelDelay         context.CancelFunc
	restoreSelection    bool
	nodeTest            *nodeTestRun
	nodeProbe           func(context.Context, Node) (int64, error)
	accountCancel       context.CancelFunc // Cancels the old account stream before logout/login.
	accountContact      time.Time          // Online freshness only; never extends offline authorization.
	syncedAPI           *API               // Login/Run share one completed initial import.
	syncCompleted       uint64
	syncError           error
	lastReportAPI       *API
	lastReport          Status
}

func New(dir string, platform Platform, engine Engine) (*Controller, error) {
	if e := platform.SecureDir(dir); e != nil {
		return nil, e
	}
	c := &Controller{dir: dir, platform: platform, engine: engine, wake: make(chan struct{}, 1)}
	c.saved = persisted{AccountVersion: 3, InstallationID: randomID(), Mode: "rule", ServerURL: DefaultServerURL, Preferences: map[string]SelectionPreference{}}
	if encrypted, e := os.ReadFile(filepath.Join(dir, "session.dat")); e == nil {
		b, e := platform.Unprotect(encrypted)
		if e != nil {
			return nil, errors.New("本机凭据解密失败")
		}
		if e = json.Unmarshal(b, &c.saved); e != nil {
			return nil, e
		}
	}
	previousServerURL := c.saved.ServerURL
	c.saved.ServerURL = migrateOfficialServerURL(c.saved.ServerURL)
	c.view = View{LastSync: c.saved.LastSync, ServerURL: c.saved.ServerURL, Status: Status{RunState: "disconnected", Mode: c.saved.Mode, TUNEnabled: c.saved.TUN, CoreVersion: CoreVersion}}
	if c.saved.AccountVersion != 3 {
		c.saved.Tokens = Tokens{}
		c.saved.Profile = Profile{}
		c.saved.Offline = nil
		c.view.OfflineUntil = nil
		c.view.ControlUnavailable = false
		c.saved.SelectedNodeID = ""
		c.saved.SelectedSubscriptionID = ""
		c.saved.Preferences = map[string]SelectionPreference{}
		c.saved.AccountVersion = 3
	}
	if c.saved.Preferences == nil {
		c.saved.Preferences = map[string]SelectionPreference{}
	}
	if c.saved.Tokens.RefreshToken != "" {
		a, e := NewAPI(c.saved.ServerURL, c.saved.Tokens)
		if e != nil {
			return nil, e
		}
		c.api = a
		c.bindTokenPersistence(a)
		c.view.LoggedIn = true
		c.view.DeviceName = c.saved.Profile.Name
		c.view.DesiredNodeID = c.saved.SelectedNodeID
		c.view.DesiredSubscriptionID = c.saved.SelectedSubscriptionID
		c.restoreSelection = true
		c.populateLocked(c.saved.Profile)
		if b, e := os.ReadFile(filepath.Join(dir, "rules.json")); e == nil {
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) == c.saved.Profile.Rules.Hash {
				_ = json.Unmarshal(b, &c.bundle)
			}
		}
	}
	c.workCtx, c.cancelWork = context.WithCancel(context.Background())
	if c.saved.ServerURL != previousServerURL {
		if err := c.persistLocked(); err != nil {
			c.cancelWork()
			return nil, err
		}
	}
	return c, nil
}
func (c *Controller) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateNodeTestLocked()
	v := c.view
	v.Nodes = append([]NodeView{}, v.Nodes...)
	v.Subscriptions = append([]Subscription{}, v.Subscriptions...)
	v.Metrics.History = append([]MetricPoint{}, v.Metrics.History...)
	v.NodeTests.Results = append([]NodeDelayResult{}, v.NodeTests.Results...)
	v.NodeChoices, _ = nodeChoices(c.saved.Profile)
	c.presentLocalChoiceLocked(&v)
	return v
}
func (c *Controller) persistLocked() error {
	if c.closed {
		return context.Canceled
	}
	if c.api != nil {
		c.saved.Tokens = c.api.Tokens()
	}
	c.saved.LastSync = c.view.LastSync
	b, e := json.Marshal(c.saved)
	if e != nil {
		return e
	}
	encrypted, e := c.platform.Protect(b)
	if e != nil {
		return e
	}
	tmp := filepath.Join(c.dir, "session.tmp")
	if e = os.WriteFile(tmp, encrypted, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, filepath.Join(c.dir, "session.dat"))
}
func (c *Controller) populateLocked(p Profile) {
	c.view.DeviceName = p.Name
	c.view.Revision = p.Revision
	c.view.RulesVersion = p.Rules.Version
	c.view.DefaultNodeID = p.DefaultNodeID
	c.view.Subscription = p.Subscription
	c.view.Subscriptions = append([]Subscription{}, p.Subscriptions...)
	c.view.SelectionRevision = p.SelectionRevision
	c.view.Nodes = []NodeView{}
	for _, n := range p.Nodes {
		c.view.Nodes = append(c.view.Nodes, NodeView{n.ID, n.Name, n.Ready})
	}
}
