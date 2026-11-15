package control

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func operationRows(t *testing.T, s *Server) []model.OperationLog {
	t.Helper()
	v, e := s.Store.ListOperationLogs(context.Background(), model.OperationLogFilter{From: s.Store.Now().Add(-time.Hour), To: s.Store.Now().Add(time.Hour), Page: 1, PageSize: 100})
	if e != nil {
		t.Fatal(e)
	}
	return v.Items
}

func TestOperationAPIRecordsTrustedActorAndSafeFields(t *testing.T) {
	s, h := newServer(t)
	cookie, csrf := loginAdmin(t, s, h)
	w := request(h, "POST", "/api/v1/password?secret=QUERY-SECRET", map[string]string{"currentPassword": "changed-password-123", "newPassword": "replacement-secret-123"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	rows := operationRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("expected login/password only: %+v", rows)
	}
	var password model.OperationLog
	for _, row := range rows {
		if row.Action == "admin.password" {
			password = row
		}
	}
	if password.ActorType != "admin" || password.ActorName != "admin" || password.IP != "198.51.100.12" || password.Route != "/api/v1/password" || password.Result != "success" {
		t.Fatalf("wrong event %+v", password)
	}
	raw, _ := json.Marshal(rows)
	for _, secret := range []string{"QUERY-SECRET", "replacement-secret-123", "changed-password-123", cookie, csrf} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret leaked")
		}
	}
	// Queries and health polling must not create an operation per page refresh.
	request(h, "GET", "/healthz", nil, "", "")
	request(h, "GET", "/api/v1/session", nil, "", "")
	if len(operationRows(t, s)) != 2 {
		t.Fatal("polling created operation logs")
	}
}

func TestOperationLogRoutesPrivateAndFiltered(t *testing.T) {
	s, h := newServer(t)
	cookie, _ := loginAdmin(t, s, h)
	w := request(h, "GET", "/api/v1/operation-logs?action=admin.login&pageSize=20", nil, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page model.OperationLogPage
	if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil || len(page.Items) != 1 {
		t.Fatal(e, w.Body.String())
	}
	id := page.Items[0].ID
	if w = request(h, "GET", "/api/v1/operation-logs/"+id, nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request(h, "GET", "/api/v1/operation-logs", nil, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w = request(s.PublicHandler(), "GET", "/api/v1/operation-logs/"+id, nil, cookie, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	for _, q := range []string{"pageSize=999", "page=0", "from=not-a-date", "from=2020-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", "ip=bogus"} {
		if w = request(h, "GET", "/api/v1/operation-logs?"+q, nil, cookie, ""); w.Code != 400 {
			t.Fatalf("%s: %d", q, w.Code)
		}
	}
}

func TestOperationUserActorAndCSRFRejection(t *testing.T) {
	s, _ := newServer(t)
	u, cookie, _ := portalLoginFixture(t, s, "log-user@example.test")
	h := s.PublicHandler()
	w := portalRequest(h, "POST", "/api/v1/user/password", map[string]string{"currentPassword": "DO-NOT-LOG", "newPassword": "ALSO-SECRET"}, cookie, "bad-csrf", s.opts.UserOrigin)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	rows := operationRows(t, s)
	if len(rows) != 1 || rows[0].ActorID != u.ID || rows[0].ActorName != u.Email || rows[0].Result != "denied" {
		t.Fatalf("wrong CSRF audit %+v", rows)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "DO-NOT-LOG") {
		t.Fatal("secret captured")
	}
}

func TestBackupStatusAndReceiptImport(t *testing.T) {
	s, h := newServer(t)
	cookie, _ := loginAdmin(t, s, h)
	w := request(h, "GET", "/api/v1/backups/status", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Body.String())
	}
	now := s.Store.Now().UTC().Truncate(time.Second)
	status := backupStatusView{Version: 1, Enabled: true, IntervalHours: 6, RetentionDays: 14, LastSuccessAt: &now, NextRunAt: &now, LastRun: &backupRun{RunID: "run-123", Status: "success", RetainedCount: 2, SizeBytes: 1234}}
	raw, _ := json.Marshal(status)
	if e := os.WriteFile(filepath.Join(s.Store.DataDir, "backup-status.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(s.Store.DataDir, "backup-events")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	event := backupEvent{Version: 1, EventID: "backup-run-123-1", RunID: "run-123", Action: "backup.completed", At: now, Result: "success", Attempt: 1, SizeBytes: 1234}
	raw, _ = json.Marshal(event)
	if e := os.WriteFile(filepath.Join(dir, "receipt-1.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	s.importBackupEvents(context.Background())
	s.importBackupEvents(context.Background())
	rows := operationRows(t, s)
	count := 0
	for _, row := range rows {
		if row.Action == "backup.completed" {
			count++
			if row.Count != 1 || row.ActorType != "system" {
				t.Fatal(row)
			}
		}
	}
	if count != 1 {
		t.Fatal("receipt replay was not idempotent", count)
	}
	w = request(h, "GET", "/api/v1/backups/status", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"retainedCount":2`) {
		t.Fatal(w.Body.String())
	}
	if conditions := s.operationHealthConditions(context.Background()); len(conditions) != 0 {
		t.Fatal(conditions)
	}
	old := now.Add(-8 * time.Hour)
	status.LastSuccessAt = &old
	raw, _ = json.Marshal(status)
	_ = os.WriteFile(filepath.Join(s.Store.DataDir, "backup-status.json"), raw, 0600)
	if conditions := s.operationHealthConditions(context.Background()); len(conditions) != 1 || conditions[0].Kind != "backup" {
		t.Fatal(conditions)
	}
	if w = request(s.PublicHandler(), http.MethodGet, "/api/v1/backups/status", nil, cookie, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestBackupStatusRejectsMalformedAndUnsafeFiles(t *testing.T) {
	s, h := newServer(t)
	cookie, _ := loginAdmin(t, s, h)
	path := filepath.Join(s.Store.DataDir, "backup-status.json")
	for _, body := range []string{`{"version":9}`, strings.Repeat("x", 65537), `{"version":1,"intervalHours":6,"retentionDays":14,"lastRun":{"runId":"x","status":"failed","errorCode":"secret=https://private"}}`} {
		if e := os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		w := request(h, "GET", "/api/v1/backups/status", nil, cookie, "")
		if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
