package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func operationTestHandler(s *Server, handler http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/example", handler)
	return s.operationRequests(mux, mux, true)
}

func operationTestRequest(h http.Handler, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/example", nil)
	r.RemoteAddr = ip
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOperationMiddlewareAggregatesAnonymousRejectsButKeepsPasswordFailures(t *testing.T) {
	s, _ := newServer(t)
	for _, status := range []int{400, 401, 403, 415, 429, 503} {
		h := operationTestHandler(s, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		for i := 0; i < 50; i++ {
			if w := operationTestRequest(h, "198.51.100.1:1234"); w.Code != status {
				t.Fatal(w.Code)
			}
		}
	}
	if got := operationRows(t, s); len(got) != 0 {
		t.Fatal("anonymous flood wrote per-request rows", got)
	}
	if len(s.operations.pending) != 6 {
		t.Fatal("unlike status codes collapsed", len(s.operations.pending))
	}
	s.flushOperationLogs(context.Background())
	rows := operationRows(t, s)
	if len(rows) != 6 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row.Count != 50 || row.Details["source"] != "198.51.100.1" {
			t.Fatal(row)
		}
	}
	h := operationTestHandler(s, func(w http.ResponseWriter, r *http.Request) {
		setOperationOutcome(r, "admin.login", "denied", map[string]any{"reason": "invalid_credentials", "failureCount": 1})
		w.WriteHeader(401)
	})
	for i := 0; i < 3; i++ {
		operationTestRequest(h, "198.51.100.1:1234")
	}
	if len(operationRows(t, s)) != 9 || len(s.operations.pending) != 0 {
		t.Fatal("bounded real password failures were not retained individually")
	}
}

func TestOperationMiddlewareAggregatesIPv6AcrossFlushes(t *testing.T) {
	s, _ := newServer(t)
	h := operationTestHandler(s, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	operationTestRequest(h, "[2001:db8:1:2::1]:1234")
	s.flushOperationLogs(context.Background())
	operationTestRequest(h, "[2001:db8:1:2::99]:1234")
	s.flushOperationLogs(context.Background())
	rows := operationRows(t, s)
	if len(rows) != 1 || rows[0].Count != 2 || rows[0].IP != "2001:db8:1:2::1" || rows[0].Details["source"] != "2001:db8:1:2::/64" {
		t.Fatal("IPv6 subject lost counts or replaced first IP sample", rows)
	}
}

func TestOperationPendingBucketsStayBoundedAcrossTimeAndSources(t *testing.T) {
	s, _ := newServer(t)
	now := s.Store.Now()
	for i := 0; i < 2500; i++ {
		at := now.Add(time.Duration(i) * 301 * time.Second)
		s.queueOperationDenial(model.OperationLog{At: at, LastAt: at, Action: fmt.Sprintf("denial.%d", i), ActorType: "anonymous", Result: "denied", IP: "198.51.100.1", Status: 403, Count: 1})
	}
	if len(s.operations.pending) > maxPendingOperationBuckets {
		t.Fatal("unbounded denial map", len(s.operations.pending))
	}
	var total int64
	for _, entry := range s.operations.pending {
		total += entry.Count
	}
	if total != 2500 {
		t.Fatal("overflow lost denied request count", total)
	}
	overflow := s.operations.pending["overflow"]
	if overflow.Action != "security.denials.overflow" || overflow.Status != 0 || overflow.IP != "" || overflow.ActorID != "" || overflow.Details["source"] != "overflow" {
		t.Fatal("overflow metadata depends on incoming request", overflow)
	}
}

func TestOperationCanceledFlushRetainsUnsubmittedBuckets(t *testing.T) {
	s, _ := newServer(t)
	now := s.Store.Now()
	s.queueOperationDenial(model.OperationLog{At: now, LastAt: now, Action: "admin.login", ActorType: "anonymous", Result: "denied", IP: "198.51.100.1", Status: 429, Count: 7})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.flushOperationLogs(ctx)
	if len(s.operations.pending) != 1 || !s.operations.failure.Load() || len(operationRows(t, s)) != 0 {
		t.Fatal("canceled flush discarded unsubmitted buckets")
	}
	s.flushOperationLogs(context.Background())
	rows := operationRows(t, s)
	if len(rows) != 1 || rows[0].Count != 7 || len(s.operations.pending) != 0 {
		t.Fatal(rows)
	}
}

func TestOperationFlushLimitsOneMaintenancePassWithoutLosingRows(t *testing.T) {
	s, _ := newServer(t)
	now := s.Store.Now()
	for i := 0; i < 130; i++ {
		s.queueOperationDenial(model.OperationLog{At: now, LastAt: now, Action: fmt.Sprintf("denial.%d", i), ActorType: "anonymous", Result: "denied", IP: "198.51.100.1", Status: 403, Count: 1})
	}
	s.flushOperationLogs(context.Background())
	if len(s.operations.pending) != 2 || s.operations.failure.Load() {
		t.Fatal("normal batch cap dropped buckets or reported false failure", len(s.operations.pending))
	}
	var count int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM operation_logs").Scan(&count); err != nil || count != 128 {
		t.Fatal(count, err)
	}
	s.flushOperationLogs(context.Background())
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM operation_logs").Scan(&count); err != nil || count != 130 || len(s.operations.pending) != 0 {
		t.Fatal(count, err)
	}
}

func TestOperationAuditFailureDoesNotChangeBusinessResultOrHideHealthFailure(t *testing.T) {
	s, _ := newServer(t)
	if _, err := s.Store.DB.Exec("ALTER TABLE operation_logs RENAME TO temporarily_missing_logs"); err != nil {
		t.Fatal(err)
	}
	h := operationTestHandler(s, func(w http.ResponseWriter, r *http.Request) {
		setOperationActor(r, "admin", "1", "admin", "session")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("business completed"))
	})
	w := operationTestRequest(h, "198.51.100.1:1234")
	if w.Code != 201 || w.Body.String() != "business completed" || !s.operations.failure.Load() {
		t.Fatal("audit failure changed business response", w.Code, w.Body.String())
	}
	if _, err := s.Store.DB.Exec("ALTER TABLE temporarily_missing_logs RENAME TO operation_logs"); err != nil {
		t.Fatal(err)
	}
	operationTestRequest(h, "198.51.100.1:1234")
	if !s.operations.failure.Load() || len(operationRows(t, s)) != 1 {
		t.Fatal("later successful receipt erased unobserved failure")
	}
}

func TestOperationMiddlewareRethrowsPanicAndDoesNotLogItsValue(t *testing.T) {
	s, _ := newServer(t)
	const secretPanic = "password=DO-NOT-RECORD"
	h := operationTestHandler(s, func(http.ResponseWriter, *http.Request) { panic(secretPanic) })
	func() {
		defer func() {
			if value := recover(); value != secretPanic {
				t.Fatalf("original panic changed: %v", value)
			}
		}()
		operationTestRequest(h, "198.51.100.1:1234")
	}()
	rows := operationRows(t, s)
	if len(rows) != 1 || rows[0].Status != 500 || rows[0].Result != "failure" || strings.Contains(fmt.Sprint(rows), "DO-NOT-RECORD") {
		t.Fatal(rows)
	}
}

type noFlushResponse struct{ header http.Header }

func (w *noFlushResponse) Header() http.Header       { return w.header }
func (*noFlushResponse) WriteHeader(int)             {}
func (*noFlushResponse) Write(p []byte) (int, error) { return len(p), nil }

func TestOperationResponsePreservesFlushAndControllerErrors(t *testing.T) {
	underlying := httptest.NewRecorder()
	w := &operationResponse{ResponseWriter: underlying}
	if err := http.NewResponseController(w).Flush(); err != nil || !underlying.Flushed || w.status != 200 {
		t.Fatal("flush not forwarded", err, underlying.Flushed, w.status)
	}
	unsupported := &operationResponse{ResponseWriter: &noFlushResponse{header: http.Header{}}}
	if err := http.NewResponseController(unsupported).Flush(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatal("flush capability error swallowed", err)
	}
}

func TestOperationTerminalLogDetailAcceptsDatabaseIdentifiers(t *testing.T) {
	s, h := newServer(t)
	cookie, _ := loginAdmin(t, s, h)
	for _, id := range []string{"job-finished:node:123", "node-finished:456", "server-finished:abc_123", "backup.finished:run-1"} {
		if err := s.Store.AppendOperationLog(context.Background(), model.OperationLog{ID: id, Action: "job.finished", Result: "success"}); err != nil {
			t.Fatal(err)
		}
		w := request(h, "GET", "/api/v1/operation-logs/"+id, nil, cookie, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"`+id+`"`) {
			t.Fatalf("terminal detail %q: %d %s", id, w.Code, w.Body.String())
		}
	}
	for _, invalid := range []string{"", "bad/id", "bad\\id", "bad id", "id?token=secret", "id\n", strings.Repeat("a", 161)} {
		if validOperationLogID(invalid) {
			t.Fatalf("accepted unsafe log key %q", invalid)
		}
	}
	if !validOperationLogID(strings.Repeat("a", 160)) || safeOperationID("job-finished:node:123") || safeOperationID("backup.finished") {
		t.Fatal("database identifier fix relaxed filesystem identifier validation")
	}
}

func TestOperationNodeLinkUsesExactEnqueuedJobDespiteNewerJob(t *testing.T) {
	s, _ := newServer(t)
	node, err := s.Store.CreateNode(model.Node{Name: "audit-exact-job", SSHHost: "node.example.com", Domain: "audit.example.com", PublicIP: "203.0.113.8", Port: 443, SSHPort: 22, SSHUser: "root"}, "ssh-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Store.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.FinishJob(first, true); err != nil {
		t.Fatal(err)
	}
	newer, err := s.Store.Queue(node.ID, "verify", "")
	if err != nil {
		t.Fatal(err)
	}
	if node.QueuedJobID != first.ID || newer.QueuedJobID <= first.ID {
		t.Fatal("enqueue results lack exact identities", node.QueuedJobID, first.ID, newer.QueuedJobID)
	}
	state := &operationContext{entry: model.OperationLog{RequestID: "original-request", ActorType: "admin", ActorID: "1", ActorName: "admin", IP: "198.51.100.1"}}
	r := httptest.NewRequest("POST", "/api/v1/nodes", nil)
	r = r.WithContext(context.WithValue(r.Context(), operationContextKey{}, state))
	s.linkNodeOperationJob(r, node.ID, node.QueuedJobID)
	want := fmt.Sprintf("node:%d", first.ID)
	if state.entry.JobID != want || state.entry.ResourceID != node.ID || state.entry.ResourceType != "node" || s.operations.failure.Load() {
		t.Fatal(state.entry)
	}
	var linked string
	if err := s.Store.DB.QueryRow("SELECT job_id FROM operation_job_context WHERE request_id=?", "original-request").Scan(&linked); err != nil || linked != want {
		t.Fatal("request linked a later job", linked, err)
	}
}

func TestOperationPaymentGetNotificationIsLoggedWithoutQuerySecrets(t *testing.T) {
	s, _ := newServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/payments/fm/notify", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	mux.HandleFunc("GET /api/v1/example", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.operationRequests(mux, mux, false)
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest("GET", "/api/v1/payments/fm/notify?sign=PRIVATE-PAYMENT-SIGNATURE&orderNo=SECRET-ORDER", nil)
		r.RemoteAddr = "198.51.100.1:1234"
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/example", nil))
	if len(operationRows(t, s)) != 0 || len(s.operations.pending) != 1 {
		t.Fatal("GET notification flood did not aggregate, or ordinary GET was logged")
	}
	s.flushOperationLogs(context.Background())
	rows := operationRows(t, s)
	if len(rows) != 1 || rows[0].Count != 3 || rows[0].Action != "payment.notify" || rows[0].Method != "GET" || rows[0].Route != "/api/v1/payments/fm/notify" {
		t.Fatal(rows)
	}
	for _, secret := range []string{"PRIVATE-PAYMENT-SIGNATURE", "SECRET-ORDER", "?sign="} {
		if strings.Contains(fmt.Sprint(rows), secret) {
			t.Fatal("payment GET query leaked into logs")
		}
	}
}
