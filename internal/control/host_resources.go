package control

import (
	"net/http"
	"sync"
	"time"

	"github.com/KeepS13/HQIP/internal/hostmetrics"
)

const hostResourceInterval = 5 * time.Second

type hostResourceView struct {
	Sample     *hostmetrics.Sample `json:"sample"`
	Refreshing bool                `json:"refreshing"`
}

type hostResourceCache struct {
	mu   sync.Mutex
	view hostResourceView
	next time.Time
	read func() hostmetrics.Sample
}

func (c *hostResourceCache) snapshot(now time.Time) hostResourceView {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.view.Refreshing && !now.Before(c.next) {
		c.view.Refreshing = true
		c.next = now.Add(hostResourceInterval)
		go func() {
			sample := c.read()
			c.mu.Lock()
			defer c.mu.Unlock()
			c.view = hostResourceView{Sample: &sample}
		}()
	}
	return c.view
}

func (s *Server) registerHostResourceRoutes(m *http.ServeMux) {
	reader := &hostmetrics.Reader{}
	cache := &hostResourceCache{read: func() hostmetrics.Sample { return reader.Read(s.Store.DataDir) }}
	m.Handle("GET /api/v1/host-resources", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, cache.snapshot(time.Now()))
	}), false))
}
