package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/provision"
)

type qualityInstaller struct {
	fakeInstaller
	fail   bool
	called int
}

func (f *qualityInstaller) NetworkQuality(ctx context.Context, in provision.Input, pin provision.PinFunc) (json.RawMessage, error) {
	f.called++
	if in.Node.PublicIP != "203.0.113.8" || in.Password == "" {
		return nil, errors.New("missing source IP or credential")
	}
	if f.fail {
		return nil, errors.New("sensitive output " + in.Password)
	}
	return json.RawMessage(`{"Head":{"IPv4":"203.0.113.8","Version":"v2026-09-16"}}`), nil
}
func TestQualityRoutesAuthCSRFAndProxyStateIsolation(t *testing.T) {
	s, h := newServer(t)
	runner := &qualityInstaller{}
	s.runner = runner
	cookie, csrf := loginAdmin(t, s, h)
	n, err := s.Store.CreateNode(model.Node{Name: "node", SSHHost: "node.example.com", Domain: "node.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.runJob(context.Background(), j)
	var installState string
	if err = s.Store.DB.QueryRow("SELECT state FROM jobs WHERE id=?", j.ID).Scan(&installState); err != nil || installState != "done" {
		t.Fatal("install must finish durably before optional quality scan", installState, err)
	}
	j, err = s.Store.NextJob()
	if err != nil || j.Kind != "quality" {
		t.Fatal("automatic scan not queued", j, err)
	}
	s.runJob(context.Background(), j)
	v, err := s.Store.NetworkQuality(n.ID)
	if err != nil || v.Status != "complete" || runner.called != 1 {
		t.Fatal(v, err, runner.called)
	}
	n, err = s.Store.Node(n.ID)
	if err != nil || n.Status != "ready" {
		t.Fatal(n, err)
	}
	path := "/api/v1/nodes/" + n.ID + "/network-quality"
	if w := request(s.PublicHandler(), "GET", path, nil, "", ""); w.Code != 404 {
		t.Fatal("public leaked report", w.Code)
	}
	if w := request(h, "GET", path, nil, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", path, nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body := map[string]any{"password": "secret", "expectedRevision": n.Revision}
	if w := request(h, "POST", path, body, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request(h, "POST", path, body, cookie, csrf); w.Code != 429 {
		t.Fatal("cooldown", w.Code, w.Body.String())
	}
	now := s.Store.Now()
	s.Store.Now = func() time.Time { return now.Add(11 * time.Minute) }
	body["expectedRevision"] = n.Revision + 1
	if w := request(h, "POST", path, body, cookie, csrf); w.Code != 409 {
		t.Fatal("conflict", w.Code)
	}
	body["expectedRevision"] = n.Revision
	runner.fail = true
	if w := request(h, "POST", path, body, cookie, csrf); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	j, err = s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.runJob(context.Background(), j)
	v, err = s.Store.NetworkQuality(n.ID)
	if err != nil || v.Status != "failed" || len(v.Report) == 0 {
		t.Fatal(v, err)
	}
	if strings.Contains(v.Error, "secret") {
		t.Fatal("runner output leaked")
	}
	password, _, _, _ := s.Store.NodeSecrets(n.ID)
	if password != "" {
		t.Fatal("manual check kept SSH password")
	}
	n, err = s.Store.Node(n.ID)
	if err != nil || n.Status != "ready" {
		t.Fatal("diagnostic failure invalidated node", n, err)
	}
	var events int
	if err = s.Store.DB.QueryRow("SELECT COUNT(*) FROM alert_job_sources WHERE job_id=?", j.ID).Scan(&events); err != nil || events != 0 {
		t.Fatal("diagnostic generated false deployment alert", err)
	}
}
