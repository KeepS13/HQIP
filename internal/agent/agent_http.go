package agent

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admission", a.admitConnection)
	mux.HandleFunc("POST /admission/check", a.admitConnection)
	mux.HandleFunc("POST /leases/{id}/renew", a.renewConnection)
	mux.HandleFunc("POST /auth", a.authenticate)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if !a.localAuthorized(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		a.liveMu.RLock()
		healthy := a.live.Healthy && a.now().Sub(a.live.SampleAt) < 3*time.Second
		a.liveMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"coreHealthy": healthy, "realityReady": a.realityReport() != nil})
	})
	return mux
}

func (a *Agent) localAuthorized(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !isLoopback(host) {
		return false
	}
	secret := r.URL.Query().Get("secret")
	if secret == "" {
		secret = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(a.cfg.StatsSecret)) == 1
}

func (a *Agent) authenticate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !a.localAuthorized(r) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false}`)
		return
	}
	var input struct {
		Address string `json:"addr"`
		Auth    string `json:"auth"`
		TX      uint64 `json:"tx"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil || len(input.Auth) > 1024 || len(input.Auth) < 16 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false}`)
		return
	}
	hash := passwordHash(input.Auth)
	result := struct {
		OK bool   `json:"ok"`
		ID string `json:"id,omitempty"`
	}{}
	a.cacheMu.RLock()
	for id, credential := range a.cache.Credentials {
		if a.cache.PortalEnabled && !strings.HasPrefix(id, "__probe_") {
			continue
		}
		if _, denied := a.cache.Pending[id]; a.cache.Retirement != nil || denied || !credential.Enabled || credentialDenied(a.cache, credential.Hash, a.now()) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(credential.Hash)) == 1 {
			result.OK, result.ID = true, id
			break
		}
	}
	// Hold the read lock through writing the authorization decision so applying
	// revocation cannot race an auth handler that has not sent its response yet.
	_ = json.NewEncoder(w).Encode(result)
	a.cacheMu.RUnlock()
}

func passwordHash(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}
