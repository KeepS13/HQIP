package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/rules"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) clientAccount(w http.ResponseWriter, r *http.Request) {
	if !s.beginPasswordWork(w, r, "legacy:"+r.PathValue("id")) {
		return
	}
	defer func() { <-s.authSlots }()
	var b struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if b.Password == "" {
		b.Password = secure.Token()
	}
	if err := s.Store.SetClientAccount(r.PathValue("id"), b.Username, b.Password); err != nil {
		fail(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]string{"username": strings.TrimSpace(b.Username), "password": b.Password})
}
func (s *Server) assignment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		NodeIDs          []string `json:"nodeIds"`
		DefaultNodeID    string   `json:"defaultNodeId"`
		ExpectedRevision int64    `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	a, err := s.Store.Assign(r.PathValue("id"), b.NodeIDs, b.DefaultNodeID, b.ExpectedRevision)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(w, 409, err.Error())
		} else {
			fail(w, 400, err.Error())
		}
		return
	}
	reply(w, 200, a)
}
func (s *Server) clientLogin(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Username       string `json:"username"`
		Password       string `json:"password"`
		InstallationID string `json:"installationId"`
		ClientName     string `json:"clientName"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if len(b.Username) > 64 || len(b.Password) > 1024 || len(b.InstallationID) < 8 || len(b.InstallationID) > 128 || !validName(b.ClientName) {
		fail(w, 400, "客户端登录参数无效")
		return
	}
	ip := "client:" + clientIP(r)
	if !s.allowLogin(w, r, ip) {
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		fail(w, 429, "登录请求繁忙，请稍后重试")
		return
	}
	tokens, err := s.Store.ClientLogin(b.Username, b.Password, b.InstallationID, b.ClientName)
	if err != nil {
		fail(w, 401, "设备账号、密码无效或设备已停用")
		return
	}
	s.mu.Lock()
	delete(s.attempts, ip)
	s.mu.Unlock()
	reply(w, 200, tokens)
}
func (s *Server) clientRefresh(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if len(b.RefreshToken) != 43 {
		fail(w, 401, "客户端登录已过期，请重新登录")
		return
	}
	tokens, err := s.Store.ClientRefresh(b.RefreshToken)
	if err != nil {
		fail(w, 401, "客户端登录已过期，请重新登录")
		return
	}
	reply(w, 200, tokens)
}
func (s *Server) clientIdentity(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || len(auth) != 50 {
		fail(w, 401, "请登录设备账号")
		return "", "", false
	}
	device, session, err := s.Store.ClientIdentity(strings.TrimPrefix(auth, "Bearer "))
	if err != nil {
		fail(w, 401, "客户端登录已过期或设备已停用")
		return "", "", false
	}
	return device, session, true
}
func (s *Server) clientLogout(w http.ResponseWriter, r *http.Request) {
	_, session, ok := s.clientIdentity(w, r)
	if !ok {
		return
	}
	err := s.Store.ClientLogout(session)
	result(w, map[string]bool{"ok": true}, err)
}
func (s *Server) clientProfile(w http.ResponseWriter, r *http.Request) {
	device, _, ok := s.clientIdentity(w, r)
	if !ok {
		return
	}
	p, err := s.Store.ClientProfile(device)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(w, 409, "设备配置正在更新，请重新获取")
			return
		}
		result(w, nil, err)
		return
	}
	raw, _ := json.Marshal(p)
	hash := sha256.Sum256(raw)
	etag := "\"" + hex.EncodeToString(hash[:]) + "\""
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	reply(w, 200, p)
}
func (s *Server) clientStatus(w http.ResponseWriter, r *http.Request) {
	device, session, ok := s.clientIdentity(w, r)
	if !ok {
		return
	}
	var status model.ClientStatus
	if !readJSON(w, r, &status) {
		return
	}
	if err := s.Store.ClientStatus(device, session, status); err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(w, 409, err.Error())
		} else {
			fail(w, 400, err.Error())
		}
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) getRules(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Rules()
	result(w, v, err)
}
func (s *Server) putRules(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Bundle rules.Bundle `json:"bundle"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	v, err := s.Store.SaveRules(b.Bundle)
	if err != nil {
		fail(w, 400, "规则校验失败："+err.Error())
		return
	}
	reply(w, 200, v)
}
func (s *Server) exportRules(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Rules()
	if err != nil {
		result(w, nil, err)
		return
	}
	raw, err := s.Store.RuleBytes(v.Hash)
	if err != nil {
		result(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=hqip-rules.json")
	_, _ = w.Write(raw)
}
func (s *Server) clientRules(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.clientIdentity(w, r)
	if !ok {
		return
	}
	hash := r.PathValue("hash")
	if len(hash) != 64 {
		fail(w, 404, "规则版本不存在")
		return
	}
	raw, err := s.Store.RuleBytes(hash)
	if err != nil {
		result(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", "\""+hash+"\"")
	_, _ = w.Write(raw)
}
