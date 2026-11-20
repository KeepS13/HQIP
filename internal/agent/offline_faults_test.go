package agent

import (
	"context"
	"encoding/json"
	"github.com/KeepS13/HQIP/internal/model"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOfflineLostAdmissionResponseReusesDurableRequest(t *testing.T) {
	now := time.Now()
	g := offlineTestGrant(now)
	var keys []string
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		keys = append(keys, in["connectionId"])
		if len(keys) == 1 {
			w.WriteHeader(503)
			return
		} // committed grant, lost response
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	d := desired(1, firstPassword, true)
	d.PortalEnabled = true
	d.OfflineAdmissionVersion = 1
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	admit := func(ch string) *httptest.ResponseRecorder {
		return admissionRequest(a, "/admission", `{"connectionId":"`+strings.Repeat(ch, 64)+`","password":"`+firstPassword+`"}`, "127.0.0.1:3000", testSecret)
	}
	if w := admit("a"); w.Code != 503 {
		t.Fatal(w.Code)
	}
	path := a.cfg.StatePath
	_ = a.ledger.close()
	var err error
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if w := admit("b"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal("retry reserved a different parent", keys)
	}
}

func TestOfflineLostReportReplaysExactSnapshotBeforeNewCounters(t *testing.T) {
	now := time.Now()
	g := offlineTestGrant(now)
	var reports []model.OfflineUsage
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u model.OfflineUsage
		_ = json.NewDecoder(r.Body).Decode(&u)
		reports = append(reports, u)
		if len(reports) == 1 {
			w.WriteHeader(503)
			return
		}
		g.ReportVersion = u.Version
		g.UploadBytes = u.UploadBytes
		g.DownloadBytes = u.DownloadBytes
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	s := offlineState{Grant: g, Children: map[string]offlineChild{}, LastWall: now}
	_, err := s.localGrant("child", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.updateChild("child", model.LeaseUsage{DownloadBytes: 100}, now); err != nil {
		t.Fatal(err)
	}
	if err = a.syncOfflineState(context.Background(), &s, true); err != nil {
		t.Fatal(err)
	}
	states, err := a.ledger.offlineStates()
	if err != nil {
		t.Fatal(err)
	}
	s = states[0]
	if err = s.updateChild("child", model.LeaseUsage{DownloadBytes: 200}, now); err != nil {
		t.Fatal(err)
	}
	if err = a.syncOfflineState(context.Background(), &s, true); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || !reflect.DeepEqual(reports[0],reports[1]) {
		t.Fatal("uncertain report mutated during retry")
	}
	if err = a.syncOfflineState(context.Background(), &s, true); err != nil {
		t.Fatal(err)
	}
	if reports[2].DownloadBytes != 200 || reports[2].Version != 2 {
		t.Fatal("new counters lost after replay")
	}
}

func TestOfflineCoreReconciliationKeepsUnconfirmedBudget(t *testing.T) {
	a := testAgent(t, "", "")
	now := a.now()
	s := offlineState{Grant: offlineTestGrant(now), Children: map[string]offlineChild{}, LastWall: now}
	_, _ = s.localGrant("old", now)
	old := s.Children["old"]
	old.CoreGeneration = "old-core"
	s.Children["old"] = old
	a.offlineBoot = time.Now()
	a.offlineIssued = map[string]time.Time{}
	a.reconcileOfflineChildren(context.Background(), &s, "old-core")
	if s.Children["old"].Ended {
		t.Fatal("restart stole unconfirmed core connection")
	}
	a.reconcileOfflineChildren(context.Background(), &s, "new-core")
	if !s.Children["old"].Ended || s.Children["old"].Closed || s.Children["old"].Normal != model.OfflineChunkBytes {
		t.Fatal("core restart freed uncertain bytes")
	}
	if _, err := s.localGrant("new", now); err != nil {
		t.Fatal("confirmed core restart could not reconnect", err)
	}
	_ = a.ledger.close()
	if err := a.ledger.saveOffline(s); err == nil {
		t.Fatal("closed ledger accepted new allocation")
	}
}
