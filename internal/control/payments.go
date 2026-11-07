package control

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
)

func (s *Server) registerPaymentAdmin(m *http.ServeMux) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/settings/payment":               s.paymentSettings,
		"PUT /api/v1/settings/payment":               s.paymentSettings,
		"GET /api/v1/orders":                         s.adminPaymentOrders,
		"GET /api/v1/orders/{id}":                    s.adminPaymentOrder,
		"POST /api/v1/orders/{id}/retry-fulfillment": s.retryPaymentOrder,
		"POST /api/v1/orders/{id}/query":             s.queryPaymentOrder,
	} {
		m.Handle(pattern, s.auth(handler, false))
	}
}

func (s *Server) registerPaymentPublic(m *http.ServeMux) {
	for pattern, handler := range map[string]portalHandler{
		"GET /api/v1/user/payment-info":                       s.userPaymentInfo,
		"GET /api/v1/user/orders":                             s.userPaymentOrders,
		"POST /api/v1/user/orders":                            s.userCreatePaymentOrder,
		"GET /api/v1/user/orders/{id}":                        s.userPaymentOrder,
		"POST /api/v1/user/orders/{id}/close":                 s.userClosePaymentOrder,
		"POST /api/v1/user/subscription-requests":             func(w http.ResponseWriter, r *http.Request, _, _ string) { stoppedApplication(w, r) },
		"POST /api/v1/user/subscription-requests/{id}/cancel": func(w http.ResponseWriter, r *http.Request, _, _ string) { stoppedApplication(w, r) },
	} {
		m.Handle(pattern, s.portalAuth("browser", handler))
	}
	m.HandleFunc("GET /api/v1/payments/fm/notify", s.paymentNotify)
	m.HandleFunc("POST /api/v1/payments/fm/notify", s.paymentNotify)
}

func stoppedApplication(w http.ResponseWriter, r *http.Request) {
	fail(w, http.StatusGone, "申请审批流程已停止，请在订阅商城购买，或联系管理员分配")
}

func (s *Server) paymentClient() *payment.Client {
	c := payment.Client{ReturnOrigin: s.opts.UserOrigin}
	if s.opts.PaymentClient != nil {
		c = *s.opts.PaymentClient
		c.ReturnOrigin = s.opts.UserOrigin
	}
	return &c
}

func (s *Server) paymentActor() string {
	name, _, _, err := s.Store.Admin()
	if err != nil {
		return "admin"
	}
	return name
}

func (s *Server) paymentSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var body model.PaymentSettingsInput
		if !readJSON(w, r, &body) {
			return
		}
		value, err := s.Store.SavePaymentSettings(body, s.paymentActor())
		portalResult(w, value, err)
		return
	}
	value, err := s.Store.PaymentSettings()
	portalResult(w, value, err)
}

func (s *Server) userPaymentInfo(w http.ResponseWriter, r *http.Request, _, _ string) {
	v, err := s.Store.PaymentSettings()
	if err != nil {
		portalResult(w, nil, err)
		return
	}
	reply(w, 200, map[string]any{"enabled": v.Enabled && v.SecretConfigured, "payType": v.PayType, "payDurationMinutes": v.PayDurationMinutes})
}

func paymentFilter(r *http.Request) (model.PaymentOrderFilter, error) {
	q := r.URL.Query()
	f := model.PaymentOrderFilter{Status: q.Get("status"), Search: strings.TrimSpace(q.Get("search")), Page: 1, PageSize: 20}
	if len(f.Search) > 120 {
		return f, fmt.Errorf("搜索内容过长")
	}
	for name, target := range map[string]*int{"page": &f.Page, "pageSize": &f.PageSize} {
		if raw := q.Get(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 1000000 {
				return f, fmt.Errorf("分页参数无效")
			}
			*target = n
		}
	}
	if f.PageSize > 100 {
		return f, fmt.Errorf("每页最多100条")
	}
	for name, target := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if raw := q.Get(name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return f, fmt.Errorf("日期格式无效")
			}
			*target = &t
		}
	}
	if f.From != nil && f.To != nil && f.From.After(*f.To) {
		return f, fmt.Errorf("日期范围无效")
	}
	return f, nil
}

func (s *Server) listPaymentOrders(w http.ResponseWriter, r *http.Request, user string) {
	f, err := paymentFilter(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	v, err := s.Store.PaymentOrders(user, f)
	portalResult(w, v, err)
}
func (s *Server) adminPaymentOrders(w http.ResponseWriter, r *http.Request) {
	s.listPaymentOrders(w, r, "")
}
func (s *Server) userPaymentOrders(w http.ResponseWriter, r *http.Request, user, _ string) {
	s.listPaymentOrders(w, r, user)
}
func (s *Server) adminPaymentOrder(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.PaymentOrder("", r.PathValue("id"))
	portalResult(w, v, e)
}
func (s *Server) userPaymentOrder(w http.ResponseWriter, r *http.Request, user, _ string) {
	v, e := s.Store.PaymentOrder(user, r.PathValue("id"))
	portalResult(w, v, e)
}

func (s *Server) userCreatePaymentOrder(w http.ResponseWriter, r *http.Request, user, _ string) {
	if !s.allowLogin(w, r, "payment-order:"+user) {
		return
	}
	var body model.PaymentOrderInput
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.Store.CreatePaymentOrder(r.Context(), user, body, s.paymentClient())
	portalResult(w, v, err)
}

func (s *Server) userClosePaymentOrder(w http.ResponseWriter, r *http.Request, user, _ string) {
	var body struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.Store.ClosePaymentOrder(user, r.PathValue("id"), body.ExpectedRevision)
	portalResult(w, v, err)
}

func (s *Server) retryPaymentOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.Store.RetryPaymentFulfillment(r.PathValue("id"), body.ExpectedRevision, s.paymentActor())
	portalResult(w, v, err)
}

func (s *Server) queryPaymentOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.Store.ReconcilePaymentOrder(r.Context(), r.PathValue("id"), body.ExpectedRevision, s.paymentActor(), s.paymentClient())
	portalResult(w, v, err)
}

func (s *Server) paymentNotify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	reject := func() { w.WriteHeader(http.StatusBadRequest); fmt.Fprint(w, "fail") }
	if len(r.URL.RawQuery) > 16<<10 {
		reject()
		return
	}
	var values url.Values
	var err error
	if r.Method == http.MethodGet {
		values, err = url.ParseQuery(r.URL.RawQuery)
	} else {
		if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
			reject()
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		err = r.ParseForm()
		values = r.Form
	}
	if err != nil || len(values) > 30 {
		reject()
		return
	}
	for _, v := range values {
		if len(v) != 1 || len(v[0]) > 2048 {
			reject()
			return
		}
	}
	if err = s.Store.ProcessPaymentNotify(values); err != nil {
		reject()
		return
	}
	fmt.Fprint(w, "success")
}

func (s *Server) RunPayments(ctx context.Context) error {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if _, err := s.Store.FulfillPaymentOrders(50); err != nil {
			log.Print("支付开通任务暂未完成，将自动重试")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
