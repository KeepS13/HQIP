package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/store"
)

// One authenticated bounded stream replaces repeated profile downloads. A
// heartbeat carries only the stable change token. Business writes already bump
// account revisions; a 15s read observes them across control instances without
// an in-memory-only event bus or a new database ledger. Native status reports,
// connection choices and traffic do not invalidate the catalog.
func (s *Server) nativeChanges(w http.ResponseWriter, r *http.Request, user, session string) {
	s.clientChangesMu.Lock()
	if s.clientChangesSessions == nil {
		s.clientChangesSessions = map[string]bool{}
	}
	if s.clientChangesSessions[session] || len(s.clientChangesSessions) >= 2048 {
		s.clientChangesMu.Unlock()
		w.Header().Set("Retry-After", "30")
		fail(w, http.StatusTooManyRequests, "变更通知连接已存在或服务繁忙")
		return
	}
	s.clientChangesSessions[session] = true
	s.clientChangesMu.Unlock()
	defer func() { s.clientChangesMu.Lock(); delete(s.clientChangesSessions, session); s.clientChangesMu.Unlock() }()
	stream := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-store")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	encoder := json.NewEncoder(w)
	for {
		// Recheck the bearer and session before sending any account information.
		// Expiry requests token rotation; a revoked refresh then signs out normally.
		owner, current, _, err := s.Store.PortalIdentity(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "native")
		if err != nil && !errors.Is(err, store.ErrUnauthorized) {
			return
		}
		message := struct {
			ChangeToken string `json:"changeToken,omitempty"`
			Reauth      bool   `json:"reauth,omitempty"`
		}{}
		message.Reauth = err != nil || owner != user || current != session
		if !message.Reauth {
			message.ChangeToken, err = s.Store.ClientChangeToken(r.Context(), user)
			if err != nil {
				return
			}
		}
		if err = stream.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return
		}
		if err = encoder.Encode(message); err != nil {
			return
		}
		if err = stream.Flush(); err != nil || message.Reauth {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
