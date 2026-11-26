package control

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/KeepS13/HQIP/internal/rules"
)

func TestPortalConfigPublishesExactRecoveryHosts(t *testing.T) {
	s, _ := newServer(t)
	s.opts.PublicURL = "https://api.example.test:8443/path"
	s.opts.UserOrigin = "https://portal.example.test"
	s.opts.AdminOrigin = "https://admin.example.test"
	w := request(s.PublicHandler(), "GET", "/api/v1/user/config", nil, "", "")
	var config struct {
		DirectAccessHosts []string `json:"directAccessHosts"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &config) != nil {
		t.Fatal("public recovery guidance unavailable", w.Code)
	}
	want := rules.ServiceBypassHosts(s.opts.PublicURL, s.opts.UserOrigin, s.opts.AdminOrigin)
	if !reflect.DeepEqual(config.DirectAccessHosts, want) {
		t.Fatal("website guidance differs from exported direct routing", config.DirectAccessHosts)
	}
}
