package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/pgstore"
)

func TestOperationLogsIdempotenceFilteringAndRedaction(t *testing.T) {
	s, now := fixture(t)
	ctx := context.Background()
	entry := model.OperationLog{ID: "event-one", At: *now, Category: "management", Action: "server.update", ActorType: "admin", ActorID: "admin", ActorName: "operator_100%", AuthType: "session", ResourceType: "server", ResourceID: "server-a", Method: "patch", Route: "/api/v1/servers/{id}", IP: "::ffff:192.0.2.1", Status: 200, Result: "success", DurationMS: 24, RequestID: "request-one", Details: map[string]any{
		"changedFields": []string{"name", "password"}, "revision": 3,
		"password": "must-never-appear", "requestBody": map[string]any{"token": "must-never-appear"},
		"stage": "https://example.test/usub/must-never-appear", "authorization": "Bearer must-never-appear",
	}}
	if err := s.AppendOperationLog(ctx, entry); err != nil {
		t.Fatal(err)
	}
	entry.Result, entry.Details = "failure", map[string]any{"stage": "changed"}
	if err := s.AppendOperationLog(ctx, entry); err != nil {
		t.Fatal(err)
	}
	got, err := s.OperationLog(ctx, entry.ID)
	if err != nil || got.Result != "success" || got.IP != "192.0.2.1" || got.Method != "PATCH" || got.Count != 1 {
		t.Fatalf("event changed or was not normalized: %+v, %v", got, err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "must-never-appear") || strings.Contains(string(raw), "requestBody") || got.Details["revision"] != float64(3) {
		t.Fatalf("unsafe details: %s", raw)
	}
	for _, filter := range []model.OperationLogFilter{
		{Actor: "operator_100%"}, {Search: "server-a"}, {Action: "server.update", Result: "success", IP: "::ffff:192.0.2.1", Method: "patch", AuthType: "session"}, {From: *now, To: *now},
	} {
		page, err := s.ListOperationLogs(ctx, filter)
		if err != nil || page.Total != 1 || len(page.Items) != 1 {
			t.Fatalf("filter %+v: %+v, %v", filter, page, err)
		}
	}
	page, err := s.ListOperationLogs(ctx, model.OperationLogFilter{Search: "%' OR 1=1 --"})
	if err != nil || page.Total != 0 {
		t.Fatal("literal search was treated as SQL", err)
	}
	if _, err = s.OperationLog(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing record", err)
	}
	if _, err = s.ListOperationLogs(ctx, model.OperationLogFilter{IP: "not-an-ip"}); err == nil {
		t.Fatal("invalid IP accepted")
	}
}

func TestOperationLogsStablePagesAndSecurityAggregation(t *testing.T) {
	s, now := fixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.AppendOperationLog(ctx, model.OperationLog{ID: fmt.Sprintf("same-time-%d", i), At: *now, Action: "node.update", Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListOperationLogs(ctx, model.OperationLogFilter{Page: 1, PageSize: 2})
	if err != nil || first.Total != 5 || len(first.Items) != 2 || first.Items[0].ID != "same-time-4" {
		t.Fatal(first, err)
	}
	second, err := s.ListOperationLogs(ctx, model.OperationLogFilter{Page: 2, PageSize: 2})
	if err != nil || len(second.Items) != 2 || second.Items[0].ID != "same-time-2" {
		t.Fatal(second, err)
	}
	bucket := model.OperationLog{ID: "security-bucket", At: *now, Action: "login.blocked", Result: "denied", IP: "2001:db8::1", Count: 1}
	if err := s.AggregateOperationLog(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	bucket.LastAt, bucket.Count = now.Add(time.Second), 3
	if err := s.AggregateOperationLog(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	bucket.IP = "2001:db8::2"
	if err := s.AggregateOperationLog(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	got, err := s.OperationLog(ctx, bucket.ID)
	if err != nil || got.Count != 4 || !got.LastAt.Equal(now.Add(time.Second)) || got.IP != "2001:db8::1" {
		t.Fatal("different security buckets merged", got, err)
	}
	bucket.ID, bucket.Count, bucket.Details = "security-prefix", 1, map[string]any{"source": "2001:db8::/64"}
	if err := s.AggregateOperationLog(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	bucket.IP = "2001:db8::3"
	if err := s.AggregateOperationLog(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	got, err = s.OperationLog(ctx, bucket.ID)
	if err != nil || got.Count != 2 || got.IP != "2001:db8::2" {
		t.Fatal("same IPv6 source prefix lost aggregated counts", got, err)
	}
}

func TestOperationLogsJobContextAndTerminalRetries(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "audit-job")
	j, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 2 {
		if err := s.FinishJob(j, true); err != nil {
			t.Fatal(err)
		}
	}
	jobID := nodeOperationJobID(j.ID)
	// A fast worker may finish before the HTTP handler links the initiator.
	if err := s.LinkOperationJob(ctx, jobID, "request-first", "admin", "operator", "Operator", "192.0.2.4"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkOperationJob(ctx, jobID, "request-second", "admin", "other", "Other", "192.0.2.5"); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListOperationLogs(ctx, model.OperationLogFilter{Search: jobID})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	got := page.Items[0]
	if got.ResourceID != n.ID || got.RequestID != "request-first" || got.ActorID != "operator" || got.IP != "192.0.2.4" || got.Result != "success" {
		t.Fatal("terminal context lost or overwritten", got)
	}
}

func TestOperationLogsRetentionIsBoundedAndSeparateFromBackup(t *testing.T) {
	s, now := fixture(t)
	ctx := context.Background()
	old := now.AddDate(0, -14, 0)
	for i := 0; i < maintenanceBatchSize+3; i++ {
		if err := s.AppendOperationLog(ctx, model.OperationLog{ID: fmt.Sprintf("old-%d", i), At: old, Action: "backup.finished", Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendOperationLog(ctx, model.OperationLog{ID: "recent", At: now.AddDate(0, -1, 0), Action: "backup.finished", Result: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MaintenanceContext(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListOperationLogs(ctx, model.OperationLogFilter{})
	if err != nil || page.Total != 4 {
		t.Fatal("retention batch was unbounded", page.Total, err)
	}
	if err := s.MaintenanceContext(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListOperationLogs(ctx, model.OperationLogFilter{})
	if err != nil || page.Total != 1 || page.Items[0].ID != "recent" {
		t.Fatal("logs incorrectly use backup's 14-day retention", page, err)
	}
}

func TestOperationLogsAdditiveSchemaPreservesLegacyAudit(t *testing.T) {
	s, _ := sqliteFixture(t)
	if _, err := s.DB.Exec("INSERT INTO audit(at,action,subject) VALUES(1,'legacy.action','legacy-resource')"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := EnsureOperationLogsSchema(s.DB); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='legacy.action'").Scan(&count); err != nil || count != 1 {
		t.Fatal("legacy audit was changed", count, err)
	}
	if _, err := s.DB.Exec("UPDATE metadata SET value='99' WHERE key='operation_logs_version'"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureOperationLogsSchema(s.DB); err == nil {
		t.Fatal("unknown future schema accepted")
	}
}

func TestPostgresOperationLogsMigrationAndApplicationQueries(t *testing.T) {
	dsn := emptyPostgresSchema(t)
	if err := InitializePostgres(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	db, err := pgstore.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{"DROP TABLE operation_logs", "DROP TABLE operation_job_context", "DELETE FROM metadata WHERE key='operation_logs_version'", "INSERT INTO audit(at,action,subject) VALUES(1,'legacy.action','retained')"} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := MigratePostgresOperationLogs(context.Background(), dsn); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{DB: db, Now: time.Now}
	if err = s.AppendOperationLog(context.Background(), model.OperationLog{ID: "pg-migration", Action: "server.update", Result: "success"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListOperationLogs(context.Background(), model.OperationLogFilter{})
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
	var retained int
	if err = db.QueryRow("SELECT COUNT(*) FROM audit WHERE action='legacy.action'").Scan(&retained); err != nil || retained != 1 {
		t.Fatal("migration changed legacy audit", retained, err)
	}
}

func TestOperationLogsCredentialCheckChangesAndLateManualLink(t *testing.T) {
	s, _, server, _ := managedServerFixture(t)
	ctx := context.Background()
	run := func(state, status string) ServerOperation {
		t.Helper()
		if _, err := s.QueueServerOperation(server.ID, "", "check", "", "", 0); err != nil {
			t.Fatal(err)
		}
		job, err := s.NextServerOperation()
		if err != nil {
			t.Fatal(err)
		}
		if err = s.FinishServerOperation(job, state, "safe fixed result", status); err != nil {
			t.Fatal(err)
		}
		return job
	}
	manual := run("succeeded", "healthy")
	page, err := s.ListOperationLogs(ctx, model.OperationLogFilter{})
	if err != nil || page.Total != 0 {
		t.Fatal("routine success flooded log", page, err)
	}
	if err = s.LinkOperationJob(ctx, manual.ID, "manual-check", "admin", "operator", "Operator", "192.0.2.4"); err != nil {
		t.Fatal(err)
	}
	run("failed", "unreachable")
	run("failed", "unreachable")
	run("succeeded", "healthy")
	page, err = s.ListOperationLogs(ctx, model.OperationLogFilter{})
	if err != nil || page.Total != 3 {
		t.Fatal("missing manual/transition or repeated failures", page, err)
	}
	got, err := s.OperationLog(ctx, "server-finished:"+manual.ID)
	if err != nil || got.ActorID != "operator" || got.RequestID != "manual-check" {
		t.Fatal("late manual check context lost", got, err)
	}
}

func TestOperationLogsPaymentReceiptAndFulfillmentAreIdempotent(t *testing.T) {
	s, _, user, template, _ := paymentFixture(t)
	order := makePaymentOrder(t, s, user, template, "audit-payment-request")
	for range 2 {
		if err := s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FulfillPaymentOrders(20); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListOperationLogs(context.Background(), model.OperationLogFilter{Search: order.ID})
	if err != nil || page.Total != 2 {
		t.Fatal("receipt/fulfillment lost or duplicated", page, err)
	}
	for _, entry := range page.Items {
		if entry.ActorType != "system" || entry.Result != "success" {
			t.Fatal(entry)
		}
	}
}

func TestOperationLogsQualityFailureDoesNotBecomeDeploymentSuccess(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "quality-log")
	if _, err := s.DB.Exec("UPDATE jobs SET state='done' WHERE node_id=?", n.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.Node(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueNetworkQuality(n.ID, "test-only-password", n.Revision); err != nil {
		t.Fatal(err)
	}
	job, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishNetworkQuality(n.ID, job.ID, "failed", "fixed test failure", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishJob(job, true); err != nil {
		t.Fatal(err)
	}
	entry, err := s.OperationLog(context.Background(), "job-finished:"+nodeOperationJobID(job.ID))
	if err != nil || entry.Result != "failure" || entry.Action != "node.quality.failed" {
		t.Fatal("failed diagnostic was reported successful", entry, err)
	}
}

func TestPostgresOperationLogsRestrictedRole(t *testing.T) {
	dsn := emptyPostgresSchema(t)
	ctx := context.Background()
	if err := InitializePostgres(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	owner, err := pgstore.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	role := "audit_app_" + NewID()
	password := "test-only-" + NewID()
	u, _ := url.Parse(dsn)
	schema := strings.Trim(u.Query().Get("search_path"), `"`)
	// CI uses password authentication; this isolated role must authenticate
	// independently of the schema owner's credentials.
	if _, err = owner.Exec("CREATE ROLE " + quotedIdentifier(role) + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '" + strings.ReplaceAll(password, "'", "''") + "'"); err != nil {
		owner.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		owner.Exec("DROP OWNED BY " + quotedIdentifier(role))
		owner.Exec("DROP ROLE " + quotedIdentifier(role))
		owner.Close()
	})
	for _, q := range []string{
		"GRANT USAGE ON SCHEMA " + quotedIdentifier(schema) + " TO " + quotedIdentifier(role),
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + quotedIdentifier(schema) + " TO " + quotedIdentifier(role),
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + quotedIdentifier(schema) + " TO " + quotedIdentifier(role),
	} {
		if _, err = owner.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	u.User = url.UserPassword(role, password)
	app, err := pgstore.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	s := &Store{DB: app, Now: time.Now}
	entry := model.OperationLog{ID: "restricted-event", Action: "login.blocked", Result: "denied", IP: "2001:db8::1", Details: map[string]any{"source": "2001:db8::/64"}}
	if err = s.AppendOperationLog(ctx, entry); err != nil {
		t.Fatal(err)
	}
	entry.IP = "2001:db8::2"
	if err = s.AggregateOperationLog(ctx, entry); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListOperationLogs(ctx, model.OperationLogFilter{})
	if err != nil || page.Total != 1 || page.Items[0].Count != 2 {
		t.Fatal(page, err)
	}
	if _, err = app.Exec("CREATE TABLE forbidden_ddl(id TEXT)"); err == nil {
		t.Fatal("application role unexpectedly has schema privileges")
	}
}

func blockOperationLogWrites(t *testing.T, s *Store) func() {
	t.Helper()
	query := "CREATE TRIGGER operation_logs_fail BEFORE INSERT ON operation_logs BEGIN SELECT RAISE(ABORT,'injected log write failure'); END"
	if s.Postgres() {
		if _, err := s.DB.Exec("CREATE FUNCTION operation_logs_fail() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RAISE EXCEPTION ''injected log write failure''; END'"); err != nil {
			t.Fatal(err)
		}
		query = "CREATE TRIGGER operation_logs_fail BEFORE INSERT ON operation_logs FOR EACH ROW EXECUTE FUNCTION operation_logs_fail()"
	}
	if _, err := s.DB.Exec(query); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		query := "DROP TRIGGER IF EXISTS operation_logs_fail"
		if s.Postgres() {
			query += " ON operation_logs"
		}
		if _, err := s.DB.Exec(query); err != nil {
			t.Error(err)
		}
		if s.Postgres() {
			if _, err := s.DB.Exec("DROP FUNCTION IF EXISTS operation_logs_fail()"); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(cleanup)
	return cleanup
}

func TestOperationLogFailureDoesNotUndoPaidOrderOrFulfillment(t *testing.T) {
	s, _, user, template, _ := paymentFixture(t)
	order := makePaymentOrder(t, s, user, template, "log-failure-payment")
	recoverWrites := blockOperationLogWrites(t, s)
	if err := s.ProcessPaymentNotify(paymentNotice(order, "test-payment-secret-v1")); err != nil {
		t.Fatal("optional log rejected a valid receipt", err)
	}
	if count, err := s.FulfillPaymentOrders(20); err != nil || count != 1 {
		t.Fatal("optional log prevented subscription grant", count, err)
	}
	paid, err := s.PaymentOrder("", order.ID)
	if err != nil || paid.PaymentState != "paid" || paid.FulfillmentState != "fulfilled" || paid.SubscriptionID == "" {
		t.Fatal(paid, err)
	}
	recoverWrites()
	if err = s.AppendOperationLog(context.Background(), model.OperationLog{Action: "test.recovered", Result: "success"}); err != nil {
		t.Fatal(err)
	}
	if !s.OperationLogWriteFailed() || !s.TakeOperationLogWriteFailure() || s.TakeOperationLogWriteFailure() {
		t.Fatal("failure was hidden by a concurrent successful write or not consumed atomically")
	}
}

func TestOperationLogFailureDoesNotUndoNodeOrServerCompletion(t *testing.T) {
	t.Run("node", func(t *testing.T) {
		s, _ := fixture(t)
		n := newNode(t, s, "failed-log-job")
		job, err := s.NextJob()
		if err != nil || n.QueuedJobID != job.ID {
			t.Fatal("created node did not return its exact queue ID", n.QueuedJobID, job.ID, err)
		}
		blockOperationLogWrites(t, s)
		if err := s.FinishJob(job, true); err != nil {
			t.Fatal("optional log rolled back node completion", err)
		}
		var state string
		if err := s.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job.ID).Scan(&state); err != nil || state != "done" || !s.TakeOperationLogWriteFailure() {
			t.Fatal(state, err)
		}
	})
	t.Run("server", func(t *testing.T) {
		s, _, server, _ := managedServerFixture(t)
		if _, err := s.QueueServerOperation(server.ID, "", "check", "", "", 0); err != nil {
			t.Fatal(err)
		}
		job, err := s.NextServerOperation()
		if err != nil {
			t.Fatal(err)
		}
		blockOperationLogWrites(t, s)
		if err = s.FinishServerOperation(job, "failed", "fixed failure", "unreachable"); err != nil {
			t.Fatal("optional log rolled back operation terminal state", err)
		}
		var state string
		if err = s.DB.QueryRow("SELECT state FROM server_operations WHERE id=?", job.ID).Scan(&state); err != nil || state != "failed" || !s.TakeOperationLogWriteFailure() {
			t.Fatal(state, err)
		}
	})
}
