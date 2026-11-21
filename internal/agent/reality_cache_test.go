package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func TestRealityQuotaCredentialOutageAndExplicitDenial(t *testing.T) {
	password := strings.Repeat("p", 43)
	status := 200
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == 200 {
			_ = json.NewEncoder(w).Encode(map[string]string{"password": password})
		}
	}))
	defer central.Close()
	a := testAgent(t, "", central.URL)
	a.cache.PortalEnabled = true
	id := model.RealityUUID(password)
	if p, err := a.resolveRealityCredential(context.Background(), id); err != nil || p != password {
		t.Fatal("online resolution", err)
	}
	var sealed string
	if err := a.ledger.db.QueryRow("SELECT secret FROM reality_credentials WHERE uuid=?", id).Scan(&sealed); err != nil || strings.Contains(sealed, password) {
		t.Fatal("credential was not sealed", err)
	}
	status = 503
	if _, err := a.resolveRealityCredential(context.Background(), id); err == nil {
		t.Fatal("ungranted credential worked offline")
	}
	s := quotaTestState(a.now(), "vless-parent", "vless", 200_000)
	s.Grant.CredentialHash = secure.Hash(password)
	if err := a.ledger.saveOffline(s); err != nil {
		t.Fatal(err)
	}
	if p, err := a.resolveRealityCredential(context.Background(), id); err != nil || p != password {
		t.Fatal("known node quota failed offline", err)
	}
	path := a.cfg.StatePath
	if err := a.ledger.close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.ledger, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.resolveRealityCredential(context.Background(), id); err != nil {
		t.Fatal("restart lost offline alias", err)
	}
	status = 403
	if _, err = a.resolveRealityCredential(context.Background(), id); err == nil {
		t.Fatal("explicit denial became outage")
	}
	status = 503
	if _, err = a.resolveRealityCredential(context.Background(), id); err == nil {
		t.Fatal("rejected credential reused on later outage")
	}
}
