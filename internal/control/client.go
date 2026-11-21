package control

import (
	"errors"
	"net/http"

	"github.com/KeepS13/HQIP/internal/rules"
	"github.com/KeepS13/HQIP/internal/store"
)

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
