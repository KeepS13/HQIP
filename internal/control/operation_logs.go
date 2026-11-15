package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

type operationContextKey struct{}

type operationContext struct {
	entry model.OperationLog
}

type operationRuntime struct {
	mu      sync.Mutex
	pending map[string]model.OperationLog
	failure atomic.Bool
}

// Only explicit, non-secret metadata is accepted. Request and response bodies
// are never passed to this function or stored as log details.
func setOperationOutcome(r *http.Request, action, result string, details map[string]any) {
	state, _ := r.Context().Value(operationContextKey{}).(*operationContext)
	if state == nil {
		return
	}
	if action != "" {
		state.entry.Action = action
	}
	if result != "" {
		state.entry.Result = result
	}
	if state.entry.Details == nil {
		state.entry.Details = map[string]any{}
	}
	for key, value := range details {
		switch key {
		case "reason", "source", "banSeconds", "failureCount", "retryAfter", "banLevel", "banStarted", "acceptedCount", "failedCount", "totalCount", "fields", "jobIds":
			state.entry.Details[key] = value
		}
	}
}

func setOperationActor(r *http.Request, kind, id, name, auth string) {
	state, _ := r.Context().Value(operationContextKey{}).(*operationContext)
	if state == nil {
		return
	}
	state.entry.ActorType, state.entry.ActorID, state.entry.ActorName, state.entry.AuthType = kind, id, name, auth
}

func operationIP(r *http.Request) string {
	ip, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return ""
	}
	return ip.Unmap().String()
}

func operationIPSubject(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "unknown"
	}
	addr = addr.Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}

func operationRoute(pattern string) string {
	_, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return ""
	}
	return path
}

func shouldRecordOperation(method, route string, admin bool) bool {
	if method == http.MethodGet && route == "/api/v1/payments/fm/notify" {
		return true
	}
	if route == "" || method == "GET" || method == "HEAD" || method == "OPTIONS" {
		return false
	}
	if !strings.HasPrefix(route, "/api/") {
		return false
	}
	if admin {
		return !strings.HasSuffix(route, "/preview") && !strings.HasSuffix(route, "/read") && !strings.HasSuffix(route, "/preflight") && !strings.HasSuffix(route, "/verification-config")
	}
	if strings.HasPrefix(route, "/api/v1/user/") {
		return !strings.Contains(route, "/authorize/")
	}
	return route == "/api/v3/client/logout" || route == "/api/v3/client/token" || route == "/api/v1/payments/fm/notify"
}

func operationAction(method, route string) string {
	switch route {
	case "/api/v1/login":
		return "admin.login"
	case "/api/v1/logout":
		return "admin.logout"
	case "/api/v1/password":
		return "admin.password"
	case "/api/v1/user/login":
		return "user.login"
	case "/api/v1/user/register":
		return "user.register"
	case "/api/v1/payments/fm/notify":
		return "payment.notify"
	}
	parts := strings.Split(strings.TrimPrefix(route, "/api/v1/"), "/")
	clean := []string{}
	for _, part := range parts {
		if part != "" && !strings.HasPrefix(part, "{") {
			clean = append(clean, part)
		}
	}
	verb := map[string]string{"POST": "create", "PUT": "update", "PATCH": "update", "DELETE": "delete"}[method]
	return strings.Join(clean, ".") + "." + verb
}

type operationResponse struct {
	http.ResponseWriter
	status int
}

func (w *operationResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *operationResponse) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *operationResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *operationResponse) Flush() {
	_ = w.FlushError()
}
func (w *operationResponse) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// A bounded transient parser extracts only known field names, never values.
// It observes bytes already consumed by the application and cannot read ahead
// or relax readJSON's size/content-type/unknown-field checks.
type operationBody struct {
	io.ReadCloser
	captured bytes.Buffer
	overflow bool
}

func (b *operationBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if !b.overflow {
		if b.captured.Len()+n > 64<<10 {
			b.overflow = true
			b.captured.Reset()
		} else {
			_, _ = b.captured.Write(p[:n])
		}
	}
	return n, err
}
func (b *operationBody) fields() []string {
	if b == nil || b.overflow {
		return nil
	}
	defer b.captured.Reset()
	var body map[string]json.RawMessage
	if json.Unmarshal(b.captured.Bytes(), &body) != nil {
		return nil
	}
	allowed := " name enabled visible email password currentPassword newPassword nodeIds serverIds domain port sshHost sshPort sshUser publicIp bandwidthMbps monthlyQuotaBytes expiresAt validityDays priceCents trafficBytes maxConnections apiBaseUrl merchantNum secretKey token registrationEnabled siteKey allowedHostnames note acknowledge sourceSHA subscriptionId templateId certificateMode zoneId defaultZoneId contactEmail rules "
	fields := []string{}
	for key := range body {
		if strings.Contains(allowed, " "+key+" ") {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return fields
}

func (s *Server) operationRequests(mux *http.ServeMux, next http.Handler, admin bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		route := operationRoute(pattern)
		if !shouldRecordOperation(r.Method, route, admin) {
			next.ServeHTTP(w, r)
			return
		}
		at, started := s.Store.Now(), time.Now()
		category := "user"
		if admin {
			category = "admin"
		}
		if strings.Contains(route, "/login") || strings.HasSuffix(route, "/logout") {
			category = "security"
		}
		state := &operationContext{entry: model.OperationLog{ID: secure.Token(), At: at, LastAt: at, Category: category, Action: operationAction(r.Method, route), ActorType: "anonymous", ActorName: "未认证", AuthType: "none", Method: r.Method, Route: route, IP: operationIP(r), RequestID: secure.Token(), Count: 1}}
		r = r.WithContext(context.WithValue(r.Context(), operationContextKey{}, state))
		var body *operationBody
		if r.Body != nil && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && category != "security" {
			body = &operationBody{ReadCloser: r.Body}
			r.Body = body
		}
		response := &operationResponse{ResponseWriter: w}
		defer func() {
			panicked := recover()
			entry := state.entry
			entry.Status = response.status
			if entry.Status == 0 {
				entry.Status = 200
			}
			if panicked != nil {
				entry.Status = 500
				entry.Result = "failure"
			}
			if entry.Result == "" {
				switch {
				case entry.Status == 202:
					entry.Result = "accepted"
				case entry.Status == 401 || entry.Status == 403 || entry.Status == 429:
					entry.Result = "denied"
				case entry.Status >= 400:
					entry.Result = "failure"
				default:
					entry.Result = "success"
				}
			}
			entry.DurationMS = time.Since(started).Milliseconds()
			entry.LastAt = s.Store.Now()
			if id := r.PathValue("id"); entry.ResourceID == "" && safeOperationID(id) {
				entry.ResourceID = id
			}
			parts := strings.Split(strings.TrimPrefix(route, "/api/v1/"), "/")
			if entry.ResourceType == "" && len(parts) > 0 {
				entry.ResourceType = parts[0]
			}
			if fields := body.fields(); len(fields) > 0 {
				if entry.Details == nil {
					entry.Details = map[string]any{}
				}
				entry.Details["fields"] = fields
			}
			if panicked == nil && aggregateOperationDenial(entry) {
				s.queueOperationDenial(entry)
			} else {
				s.writeOperation(r.Context(), entry, false)
			}
			if panicked != nil {
				panic(panicked)
			}
		}()
		next.ServeHTTP(response, r)
	})
}

func aggregateOperationDenial(entry model.OperationLog) bool {
	if entry.Status == http.StatusTooManyRequests {
		return true
	}
	if entry.ActorType != "anonymous" || entry.Status < http.StatusBadRequest || entry.Status > 599 {
		return false
	}
	// Actual password failures are already bounded by the shared 30/minute
	// budget. Preserve each failure and its ban transition; unauthenticated
	// Origin/session/payload rejections and dependency outages can happen before
	// that guard and aggregate, including bad payment notifications and Redis 503.
	if entry.Action == "admin.login" && entry.Status == http.StatusUnauthorized {
		if failures, ok := entry.Details["failureCount"].(int); ok && failures > 0 {
			return false
		}
	}
	return true
}

func safeOperationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Server) writeOperation(parent context.Context, entry model.OperationLog, aggregate bool) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer cancel()
	var err error
	if aggregate {
		err = s.Store.AggregateOperationLog(ctx, entry)
	} else {
		err = s.Store.AppendOperationLog(ctx, entry)
	}
	if err != nil {
		if !s.operations.failure.Swap(true) {
			log.Print("HQIP 操作日志写入失败，请检查存储；业务结果保持原样")
		}
		return false
	}
	return true
}

const maxPendingOperationBuckets = 1024

func (s *Server) queueOperationDenial(entry model.OperationLog) {
	details := make(map[string]any, len(entry.Details)+1)
	for key, value := range entry.Details {
		details[key] = value
	}
	source := operationIPSubject(entry.IP)
	details["source"] = source
	entry.Details = details
	reason, _ := details["reason"].(string)
	bucket := strconv.FormatInt(entry.At.Unix()/300, 10)
	// Route, status and actor are part of the identity so unlike outcomes never
	// silently collapse. IP is deliberately represented by its /64 subject.
	identity, _ := json.Marshal([]any{bucket, source, entry.Action, reason, entry.Status, entry.Result, entry.ActorType, entry.ActorID, entry.AuthType, entry.Method, entry.Route})
	entry.ID = "rate-" + secure.Hash(string(identity))
	entry.RequestID = ""
	s.operations.mu.Lock()
	defer s.operations.mu.Unlock()
	s.mergeOperationBucketLocked(entry)
}

// The last slot is reserved for a single overflow aggregate, even when a
// disabled/stalled maintenance worker leaves several five-minute buckets in
// memory. Its canonical metadata also stays stable across independent flushes.
func (s *Server) mergeOperationBucketLocked(entry model.OperationLog) {
	if s.operations.pending == nil {
		s.operations.pending = map[string]model.OperationLog{}
	}
	key := entry.ID
	if entry.Action == "security.denials.overflow" {
		key = "overflow"
	} else if _, exists := s.operations.pending[key]; !exists && len(s.operations.pending) >= maxPendingOperationBuckets-1 {
		key = "overflow"
		entry = model.OperationLog{
			ID: "rate-" + secure.Hash(strconv.FormatInt(entry.At.Unix()/300, 10)+":overflow"),
			At: entry.At, LastAt: entry.LastAt, Category: "security", Action: "security.denials.overflow",
			ActorType: "anonymous", ActorName: "未认证", AuthType: "none", Result: "denied", Count: entry.Count,
			Details: map[string]any{"reason": "overflow", "source": "overflow"},
		}
	}
	if entry.Count < 1 {
		entry.Count = 1
	}
	if old, exists := s.operations.pending[key]; exists {
		old.Count += entry.Count
		if entry.LastAt.After(old.LastAt) {
			old.LastAt = entry.LastAt
		}
		if entry.At.Before(old.At) {
			old.At = entry.At
		}
		s.operations.pending[key] = old
		return
	}
	s.operations.pending[key] = entry
}

func (s *Server) flushOperationLogs(ctx context.Context) {
	batchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.operations.mu.Lock()
	pending := s.operations.pending
	s.operations.pending = map[string]model.OperationLog{}
	s.operations.mu.Unlock()
	keys := make([]string, 0, len(pending))
	for key := range pending {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := pending[keys[i]], pending[keys[j]]
		if a.At.Equal(b.At) {
			return keys[i] < keys[j]
		}
		return a.At.Before(b.At)
	})
	for i, key := range keys {
		if i >= 128 || batchCtx.Err() != nil {
			// These buckets were never submitted, so retaining them is safe.
			// An attempted write has an uncertain outcome and must not be retried
			// because AggregateOperationLog increments an existing count.
			s.operations.mu.Lock()
			for _, remaining := range keys[i:] {
				s.mergeOperationBucketLocked(pending[remaining])
			}
			s.operations.mu.Unlock()
			if batchCtx.Err() != nil {
				s.operations.failure.Store(true)
			}
			return
		}
		s.writeOperation(batchCtx, pending[key], true)
	}
}

func (s *Server) linkOperationJob(r *http.Request, jobID string) {
	state, _ := r.Context().Value(operationContextKey{}).(*operationContext)
	if state == nil {
		return
	}
	entry := &state.entry
	entry.JobID = jobID
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer cancel()
	if err := s.Store.LinkOperationJob(ctx, jobID, entry.RequestID, entry.ActorType, entry.ActorID, entry.ActorName, entry.IP); err != nil {
		s.operations.failure.Store(true)
		log.Print("HQIP 运维日志关联失败")
	}
}

func (s *Server) linkNodeOperationJob(r *http.Request, nodeID string, queuedJobID int64) {
	state, _ := r.Context().Value(operationContextKey{}).(*operationContext)
	if state == nil {
		return
	}
	entry := &state.entry
	entry.ResourceType, entry.ResourceID = "node", nodeID
	if queuedJobID < 1 {
		if !s.operations.failure.Swap(true) {
			log.Print("HQIP 节点运维日志缺少任务编号")
		}
		return
	}
	jobID := "node:" + strconv.FormatInt(queuedJobID, 10)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer cancel()
	entry.JobID = jobID
	if err := s.Store.LinkOperationJob(ctx, jobID, entry.RequestID, entry.ActorType, entry.ActorID, entry.ActorName, entry.IP); err != nil {
		if !s.operations.failure.Swap(true) {
			log.Print("HQIP 节点运维日志关联失败")
		}
	}
}

func (s *Server) registerOperationLogs(m *http.ServeMux) {
	m.Handle("GET /api/v1/operation-logs", s.auth(http.HandlerFunc(s.listOperationLogs), false))
	m.Handle("GET /api/v1/operation-logs/{id}", s.auth(http.HandlerFunc(s.operationLogDetail), false))
	m.Handle("GET /api/v1/backups/status", s.auth(http.HandlerFunc(s.backupStatus), false))
}
func (s *Server) listOperationLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := model.OperationLogFilter{Search: q.Get("search"), Actor: q.Get("actor"), Action: q.Get("action"), IP: q.Get("ip"), Method: q.Get("method"), AuthType: q.Get("authType"), Result: q.Get("result"), Page: 1, PageSize: 20, From: s.Store.Now().Add(-7 * 24 * time.Hour), To: s.Store.Now()}
	for _, key := range []string{"search", "actor", "action", "ip", "method", "authType", "result"} {
		if len(q.Get(key)) > 200 || strings.ContainsAny(q.Get(key), "\x00\r\n") {
			fail(w, 400, "筛选内容无效")
			return
		}
	}
	for key, dst := range map[string]*int{"page": &f.Page, "pageSize": &f.PageSize} {
		if v := q.Get(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 100000 {
				fail(w, 400, "分页参数无效")
				return
			}
			*dst = n
		}
	}
	if f.PageSize != 20 && f.PageSize != 50 && f.PageSize != 100 {
		fail(w, 400, "每页数量无效")
		return
	}
	for key, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				fail(w, 400, "时间范围无效")
				return
			}
			*dst = t
		}
	}
	if f.To.Before(f.From) || f.To.Sub(f.From) > 400*24*time.Hour {
		fail(w, 400, "时间范围不能超过 400 天")
		return
	}
	if f.IP != "" {
		ip, err := netip.ParseAddr(f.IP)
		if err != nil {
			fail(w, 400, "客户端 IP 无效")
			return
		}
		f.IP = ip.Unmap().String()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	v, err := s.Store.ListOperationLogs(ctx, f)
	result(w, v, err)
}
func (s *Server) operationLogDetail(w http.ResponseWriter, r *http.Request) {
	if !validOperationLogID(r.PathValue("id")) {
		fail(w, 400, "记录编号无效")
		return
	}
	v, err := s.Store.OperationLog(r.Context(), r.PathValue("id"))
	result(w, v, err)
}

// Log identifiers are database keys, not filesystem names. Keep this separate
// from safeOperationID, which protects backup receipt filenames and paths.
func validOperationLogID(value string) bool {
	if value == "" || len(value) > 160 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}
