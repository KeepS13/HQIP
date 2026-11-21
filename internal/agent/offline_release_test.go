package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestRetiredLegacyRetainsEndedChildBudget(t *testing.T) {
	s := offlineState{Grant: offlineTestGrant(time.Now()), Denied: true, Children: map[string]offlineChild{
		"closed":  {Closed: true, Normal: 100, Download: 100},
		"unknown": {Ended: true, Normal: 1000, Download: 200},
	}}
	if idleLegacyGrant(&s) {
		t.Fatal("old controller cannot shrink uncertain parents")
	}
	units, err := retainedLegacyUnits(&s)
	if err != nil || units == nil || *units != 110_000 {
		t.Fatal("uncertain allocation was returned", units, err)
	}
	child := s.Children["unknown"]
	child.Ended = false
	s.Children["unknown"] = child
	if units, err = retainedLegacyUnits(&s); err != nil || units != nil {
		t.Fatal("active parent retired", units, err)
	}
}

func TestRetiredLegacyAcknowledgementPersistsUncertainChild(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	g := offlineTestGrant(now)
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u model.OfflineUsage
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if u.RetainedNormalUnits == nil || *u.RetainedNormalUnits != 110_000 || u.Active || u.Closed {
			t.Errorf("unsafe retirement report %+v", u)
		}
		g.ReportVersion = u.Version
		g.UploadBytes = u.UploadBytes
		g.DownloadBytes = u.DownloadBytes
		g.Revoked = true
		g.OfflineUntil = now
		g.NormalBudgetBytes = 1100
		_ = json.NewEncoder(w).Encode(g)
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.now = func() time.Time { return now }
	a.cache.NodeQuotaVersion = 1
	s := offlineState{Grant: g, LastWall: now, Children: map[string]offlineChild{
		"closed": {Closed: true, Normal: 100, Download: 100}, "unknown": {Ended: true, Normal: 1000, Download: 200},
	}}
	if err := a.retireIdleLegacyGrant(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := a.ledger.offlineChild("unknown")
	if err != nil || !ok || !stored.Denied || stored.Grant.Closed || stored.Children["unknown"].Closed || stored.Children["unknown"].Normal != 1000 || stored.Pending != nil {
		t.Fatal("uncertain allocation or retirement acknowledgement lost", stored, err)
	}
}
