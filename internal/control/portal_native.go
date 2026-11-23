package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

// Native client endpoints retain their independent session and credential scope.
func (s *Server) nativeBegin(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(w, r, "native-authorize:"+clientIP(r)) {
		return
	}
	var b struct {
		CodeChallenge  string `json:"codeChallenge"`
		RedirectURI    string `json:"redirectUri"`
		State          string `json:"state"`
		ClientName     string `json:"clientName"`
		InstallationID string `json:"installationId"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	id, expires, err := s.Store.BeginPortalAuthorization(b.CodeChallenge, b.RedirectURI, b.State, b.ClientName, b.InstallationID)
	portalResult(w, map[string]any{"requestId": id, "authorizationUrl": s.opts.UserOrigin + "/authorize?request=" + id, "expiresAt": expires}, err)
}
func (s *Server) nativeToken(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(w, r, "native-token:"+clientIP(r)) {
		return
	}
	var b struct {
		Code           string `json:"code"`
		CodeVerifier   string `json:"codeVerifier"`
		RedirectURI    string `json:"redirectUri"`
		InstallationID string `json:"installationId"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	t, e := s.Store.ExchangePortalCode(b.Code, b.CodeVerifier, b.RedirectURI, b.InstallationID)
	if e == nil {
		setOperationActor(r, "user", t.User.ID, t.User.Email, "native")
	}
	portalResult(w, t, e)
}
func (s *Server) nativeRefresh(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RefreshToken string `json:"refreshToken"`
		RequestID    string `json:"requestId"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	t, e := s.Store.PortalRefreshRequest(b.RefreshToken, b.RequestID)
	if e != nil && !errors.Is(e, store.ErrUnauthorized) {
		w.Header().Set("Retry-After", "5")
		fail(w, 503, "账户服务暂时不可用，请稍后重试")
		return
	}
	portalResult(w, t, e)
}
func (s *Server) nativeProfile(w http.ResponseWriter, r *http.Request, user, session string) {
	id, selectionRevision, err := s.Store.SelectedSubscription(user, session)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	p, e, err := s.Store.PortalClientProfile(user, session, id)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	u, err := s.Store.PortalUser(user)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	p.SubscriptionID = id
	p.SelectionRevision = selectionRevision
	availableNodes, err := s.Store.ClientNodeCatalog(u.Subscriptions)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	// Older native clients interpret every endpoint as HY2. Their existing
	// sessions keep the old shape until the client advertises protocol support.
	if !nativeProtocolSupported(r, model.RealityCapability) {
		filtered := p.Nodes[:0]
		for _, n := range p.Nodes {
			if n.Protocol != "vless" {
				filtered = append(filtered, n)
			}
		}
		p.Nodes = filtered
		catalog := availableNodes[:0]
		for _, n := range availableNodes {
			if n.Protocol != "vless" {
				catalog = append(catalog, n)
			}
		}
		availableNodes = catalog
	}
	latestID, latestSelection, err := s.Store.SelectedSubscription(user, session)
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	if latestID != id || latestSelection != selectionRevision {
		portalResult(w, nil, store.ErrConflict)
		return
	}
	v := struct {
		model.ClientProfile
		User           model.PortalUser           `json:"user"`
		Subscription   *model.UserSubscription    `json:"subscription"`
		Subscriptions  []model.UserSubscription   `json:"subscriptions"`
		AvailableNodes []model.ClientNodeEndpoint `json:"availableNodes"`
	}{p, u, e, u.Subscriptions, availableNodes}
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	etag := "\"" + hex.EncodeToString(sum[:]) + "\""
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	reply(w, 200, v)
}

// Capabilities are a set, not an application version. New clients may advertise
// additional protocols without losing ones this server already understands.
func nativeProtocolSupported(r *http.Request, capability string) bool {
	for _, value := range r.URL.Query()["protocols"] {
		for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			if item == capability {
				return true
			}
		}
	}
	return false
}
func (s *Server) nativeStatus(w http.ResponseWriter, r *http.Request, user, session string) {
	var b model.ClientStatus
	if !readJSON(w, r, &b) {
		return
	}
	if len(b.LastError) > 2000 || len(b.CoreVersion) > 100 || len(b.RunState) > 40 {
		fail(w, 400, "状态字段过长")
		return
	}
	raw, _ := json.Marshal(b)
	_, err := s.Store.DB.Exec("UPDATE portal_sessions SET status=? WHERE id=? AND user_id=?", string(raw), session, user)
	portalResult(w, map[string]bool{"ok": true}, err)
}
func (s *Server) nativeRules(w http.ResponseWriter, r *http.Request, user, session string) {
	raw, err := s.Store.RuleBytes(r.PathValue("hash"))
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
func (s *Server) nativeDrain(w http.ResponseWriter, r *http.Request, user, session string) {
	var b struct {
		SubscriptionID   string `json:"subscriptionId"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	portalResult(w, map[string]bool{"ok": true}, s.Store.DrainSubscriptionSession(user, session, b.SubscriptionID, b.ExpectedRevision))
}
