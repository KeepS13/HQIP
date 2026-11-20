package control

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNodeAPIIgnoresRetiredCapacityFields(t *testing.T) {
	s, handler := newServer(t)
	cookie, csrf := loginAdmin(t, s, handler)
	w := request(handler, "POST", "/api/v1/nodes", map[string]any{
		"name": "meter-only", "sshHost": "8.8.8.9", "sshUser": "root", "sshPort": 22,
		"publicIp": "8.8.8.9", "domain": "meter.example.com", "port": 443, "password": "ssh-secret",
		"bandwidthMbps": 1, "monthlyQuotaBytes": 10,
	}, cookie, csrf)
	if w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var n model.Node
	if err := json.Unmarshal(w.Body.Bytes(), &n); err != nil || n.BandwidthMbps != nil || n.MonthlyQuotaBytes != nil {
		t.Fatalf("create retained limits: %+v, %v", n, err)
	}
	desired := n.DesiredRevision
	w = request(handler, "PATCH", "/api/v1/nodes/"+n.ID, map[string]any{"name": "renamed", "bandwidthMbps": 2, "monthlyQuotaBytes": 5}, cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &n); err != nil || n.Name != "renamed" || n.BandwidthMbps != nil || n.MonthlyQuotaBytes != nil || n.DesiredRevision != desired {
		t.Fatalf("rename reinstated limits or redeployed: %+v, %v", n, err)
	}
	if w = request(handler, "PATCH", "/api/v1/nodes/"+n.ID, map[string]any{"domain": "elsewhere.example.com"}, cookie, csrf); w.Code != 400 {
		t.Fatalf("metadata endpoint allowed topology edits: %d", w.Code)
	}
}
