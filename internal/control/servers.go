package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/provision"
	"github.com/KeepS13/HQIP/internal/store"
)

type serverInspector interface {
	CheckServer(context.Context, model.Server, string, provision.PinFunc) (provision.ServerInspection, error)
	Inspect(context.Context, provision.Input, provision.LogFunc, provision.PinFunc) (provision.PreflightResult, error)
}

type serverForm struct {
	Name              string     `json:"name"`
	SSHHost           string     `json:"sshHost"`
	SSHPort           int        `json:"sshPort"`
	SSHUser           string     `json:"sshUser"`
	Password          string     `json:"password"`
	ExpectedRevision  int64      `json:"expectedRevision"`
	BandwidthMbps     *float64   `json:"bandwidthMbps"`
	MonthlyQuotaBytes *int64     `json:"monthlyQuotaBytes"`
	ExpiresAt         *time.Time `json:"expiresAt"`
}

func (s *Server) registerServerRoutes(m *http.ServeMux) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/servers": s.listServers, "POST /api/v1/servers": s.createServer,
		"GET /api/v1/servers/{id}": s.serverDetail, "PATCH /api/v1/servers/{id}": s.updateServer, "DELETE /api/v1/servers/{id}": s.deleteServer,
		"PATCH /api/v1/servers/{id}/settings": s.updateServerSettings,
		"GET /api/v1/servers/{id}/traffic":    s.serverTraffic,
		"GET /api/v1/servers/{id}/renewals":   s.serverRenewals, "POST /api/v1/servers/{id}/renewals": s.renewServer,
		"GET /api/v1/servers/{id}/addresses": s.listAddresses, "POST /api/v1/servers/{id}/addresses": s.createAddress, "DELETE /api/v1/servers/{id}/addresses/{address}": s.deleteAddress,
		"POST /api/v1/nodes/{id}/preflight": s.nodePreflight, "POST /api/v1/nodes/{id}/takeover": s.retryNode,
		"GET /api/v1/nodes/{id}/delete-preview": s.nodeDeletePreview, "DELETE /api/v1/nodes/{id}": s.deleteNode,
		"POST /api/v1/nodes/{id}/retire": s.retireNode, "DELETE /api/v1/devices/{id}": s.deleteDevice,
	} {
		m.Handle(pattern, s.auth(handler, false))
	}
}

func managementError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(w, 404, "记录不存在")
		return
	}
	fail(w, 409, err.Error())
}

func (f *serverForm) validate() error {
	f.Name = strings.TrimSpace(f.Name)
	f.SSHHost = strings.TrimSpace(f.SSHHost)
	f.SSHUser = strings.TrimSpace(f.SSHUser)
	if f.SSHPort == 0 {
		f.SSHPort = 22
	}
	if f.SSHUser == "" {
		f.SSHUser = "root"
	}
	if !validName(f.Name) {
		return errors.New("服务器名称应为 1–80 个字符")
	}
	if len(f.SSHHost) > 253 || (net.ParseIP(f.SSHHost) == nil && !domainRE.MatchString(f.SSHHost)) {
		return errors.New("SSH 地址必须为 IP 或完整域名")
	}
	if !userRE.MatchString(f.SSHUser) || f.SSHPort < 1 || f.SSHPort > 65535 {
		return errors.New("SSH 用户或端口格式无效")
	}
	if len(f.Password) < 1 || len(f.Password) > 1024 {
		return errors.New("请填写本次验证使用的 SSH 密码")
	}
	if !validQuota(f.BandwidthMbps, f.MonthlyQuotaBytes) {
		return errors.New("带宽和流量额度必须为正数；无限请填写 null")
	}
	return nil
}

func (s *Server) inspectServer(r *http.Request, f serverForm, current model.Server) (model.Server, error) {
	inspector, ok := s.runner.(serverInspector)
	if !ok {
		return current, errors.New("当前部署组件不支持服务器检查")
	}
	current.Name = f.Name
	current.SSHHost = f.SSHHost
	current.SSHPort = f.SSHPort
	current.SSHUser = f.SSHUser
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	oldMachine := current.MachineID
	inspection, err := inspector.CheckServer(ctx, current, f.Password, func(pin string) error {
		if current.HostKey != "" && current.HostKey != pin {
			return errors.New("SSH 主机指纹不匹配")
		}
		current.HostKey = pin
		return nil
	})
	if err != nil {
		return current, errors.New("SSH 或主机身份检查失败：" + strings.ReplaceAll(err.Error(), f.Password, "[已隐藏]"))
	}
	if oldMachine != "" && oldMachine != inspection.MachineID {
		return current, errors.New("管理地址对应了另一台服务器，拒绝更改")
	}
	current.MachineID = inspection.MachineID
	current.Platform = inspection.Platform
	current.Architecture = inspection.Architecture
	return current, nil
}

func (s *Server) listServers(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.Servers()
	result(w, v, e)
}
func (s *Server) createServer(w http.ResponseWriter, r *http.Request) {
	var f serverForm
	if !readJSON(w, r, &f) {
		return
	}
	if err := f.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if f.ExpiresAt == nil || !serverExpiryInput(*f.ExpiresAt, s.Store.Now()) {
		fail(w, 400, "请选择未来的北京时间整点到期时间")
		return
	}
	checked, err := s.inspectServer(r, f, model.Server{})
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	checked.BandwidthMbps, checked.MonthlyQuotaBytes = f.BandwidthMbps, f.MonthlyQuotaBytes
	checked.Billing.ExpiresAt = f.ExpiresAt
	actor, _, _, err := s.Store.Admin()
	if err != nil {
		result(w, nil, err)
		return
	}
	v, err := s.Store.CreateServer(checked, actor)
	if err != nil {
		managementError(w, err)
		return
	}
	reply(w, 201, v)
}
func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	var f serverForm
	if !readJSON(w, r, &f) {
		return
	}
	if f.ExpiresAt != nil {
		fail(w, 400, "到期时间请通过首次配置或续费操作调整")
		return
	}
	if err := f.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	current, err := s.Store.Server(r.PathValue("id"))
	if err != nil {
		managementError(w, err)
		return
	}
	if current.Revision != f.ExpectedRevision {
		managementError(w, store.ErrConflict)
		return
	}
	checked, err := s.inspectServer(r, f, current)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	v, err := s.Store.UpdateServer(checked, f.ExpectedRevision)
	if err != nil {
		managementError(w, err)
		return
	}
	reply(w, 200, v)
}
func (s *Server) serverDetail(w http.ResponseWriter, r *http.Request) {
	server, err := s.Store.Server(r.PathValue("id"))
	if err != nil {
		managementError(w, err)
		return
	}
	addresses, err := s.Store.Addresses(server.ID)
	if err != nil {
		result(w, nil, err)
		return
	}
	nodes, err := s.Store.ServerNodeUsage(server.ID)
	if err != nil {
		result(w, nil, err)
		return
	}
	filtered := []model.Node{}
	for _, n := range nodes {
		if n.Lifecycle != "deleted" {
			filtered = append(filtered, n)
		}
	}
	reply(w, 200, map[string]any{"server": server, "addresses": addresses, "nodes": filtered, "nodeUsage": nodes})
}

// Settings never call SSH or mutate node configuration. Both nullable quotas
// must be explicit so a missing form field cannot silently reset a capacity.
func (s *Server) updateServerSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name              string          `json:"name"`
		BandwidthMbps     json.RawMessage `json:"bandwidthMbps"`
		MonthlyQuotaBytes json.RawMessage `json:"monthlyQuotaBytes"`
		ExpectedRevision  int64           `json:"expectedRevision"`
		ExpiresAt         *time.Time      `json:"expiresAt"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	var bandwidth *float64
	var quota *int64
	if !validName(name) || body.ExpectedRevision < 1 ||
		len(body.BandwidthMbps) == 0 || len(body.MonthlyQuotaBytes) == 0 ||
		json.Unmarshal(body.BandwidthMbps, &bandwidth) != nil ||
		json.Unmarshal(body.MonthlyQuotaBytes, &quota) != nil || !validQuota(bandwidth, quota) {
		fail(w, 400, "请填写有效名称、版本及正数额度；无限请填写 null")
		return
	}
	if body.ExpiresAt != nil && !serverExpiryInput(*body.ExpiresAt, s.Store.Now()) {
		fail(w, 400, "请选择未来的北京时间整点到期时间")
		return
	}
	actor, _, _, err := s.Store.Admin()
	if err != nil {
		result(w, nil, err)
		return
	}
	v, err := s.Store.UpdateServerSettings(r.PathValue("id"), name, bandwidth, quota, body.ExpectedRevision, store.ServerBillingSetup{ExpiresAt: body.ExpiresAt, Actor: actor})
	if errors.Is(err, store.ErrConflict) {
		fail(w, 409, "服务器设置已被修改，请刷新后重试")
		return
	}
	result(w, v, err)
}

func serverExpiryInput(at, now time.Time) bool {
	return at.After(now) && at.Year() <= 2200 && at.Unix()%3600 == 0 && at.Nanosecond() == 0
}

func (s *Server) serverRenewals(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ServerRenewals(r.PathValue("id"))
	result(w, v, err)
}

func (s *Server) renewServer(w http.ResponseWriter, r *http.Request) {
	var b store.ServerRenewalRequest
	if !readJSON(w, r, &b) {
		return
	}
	if b.ExpectedRevision < 1 || len(b.RequestID) < 8 || len(b.RequestID) > 128 || strings.ContainsAny(b.RequestID, "\r\n\x00") || (b.Months == nil) == (b.ExpiresAt == nil) || b.Months != nil && (*b.Months < 1 || *b.Months > 1200) {
		fail(w, 400, "请提供版本、请求标识，并选择续费月数或到期时间")
		return
	}
	// Future-date validation is transactional in Store, after idempotent replay.
	// A successful manual renewal remains retryable after its date has passed.
	actor, _, _, err := s.Store.Admin()
	if err != nil {
		result(w, nil, err)
		return
	}
	v, renewal, err := s.Store.RenewServer(r.PathValue("id"), b, actor)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, 404, "服务器不存在")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, 409, "服务器或续费请求已发生变化，请刷新后重试")
		return
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]any{"server": v, "renewal": renewal})
}

func (s *Server) serverTraffic(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}
	if period != "24h" && period != "7d" {
		fail(w, 400, "请选择最近 24 小时或 7 天")
		return
	}
	if _, err := s.Store.Server(r.PathValue("id")); err != nil {
		result(w, nil, err)
		return
	}
	points, err := s.Store.ServerTraffic(r.PathValue("id"), period)
	result(w, points, err)
}
func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if err := s.Store.DeleteServer(r.PathValue("id"), b.ExpectedRevision); err != nil {
		managementError(w, err)
		return
	}
	reply(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) listAddresses(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.Addresses(r.PathValue("id"))
	result(w, v, e)
}
func (s *Server) createAddress(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PublicIP string `json:"publicIp"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	v, err := s.Store.CreateAddress(r.PathValue("id"), strings.TrimSpace(b.PublicIP))
	if err != nil {
		managementError(w, err)
		return
	}
	reply(w, 201, v)
}
func (s *Server) deleteAddress(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteAddress(r.PathValue("id"), r.PathValue("address")); err != nil {
		managementError(w, err)
		return
	}
	reply(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) nodePreflight(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Password        string `json:"password"`
		CertificateMode string `json:"certificateMode"`
		ZoneID          string `json:"zoneId"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if len(b.Password) < 1 || len(b.Password) > 1024 {
		fail(w, 400, "请填写 SSH 密码")
		return
	}
	n, err := s.Store.Node(r.PathValue("id"))
	if err != nil {
		managementError(w, err)
		return
	}
	if n.Lifecycle == "retiring" || n.Lifecycle == "deleted" {
		fail(w, 409, "节点正在删除或已删除")
		return
	}
	if b.CertificateMode != "" {
		if b.CertificateMode != "http" && b.CertificateMode != "cloudflare" {
			fail(w, 400, "证书模式无效")
			return
		}
		n.CertificateMode = b.CertificateMode
		n.ZoneID = b.ZoneID
	}
	inspector, ok := s.runner.(serverInspector)
	if !ok {
		fail(w, 503, "当前部署组件不支持预检")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	check, err := inspector.Inspect(ctx, provision.Input{Node: n, Password: b.Password}, func(stage, message string) {}, func(pin string) error { return s.Store.Pin(n.ID, pin) })
	if err != nil {
		fail(w, 400, strings.ReplaceAll(err.Error(), b.Password, "[已隐藏]"))
		return
	}
	if err = s.Store.SavePreflight(n.ID, check.Fingerprint, check.CanTakeover); err != nil {
		result(w, nil, err)
		return
	}
	reply(w, 200, check)
}

type deletionDevice struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Assignment model.Assignment `json:"assignment"`
	Candidates []model.Node     `json:"candidates"`
}

func (s *Server) nodeDeletePreview(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.Node(r.PathValue("id"))
	if err != nil {
		managementError(w, err)
		return
	}
	templates, err := s.Store.NodeSubscriptionReferences(n.ID)
	if err != nil {
		result(w, nil, err)
		return
	}
	devices, err := s.Store.Devices()
	if err != nil {
		result(w, nil, err)
		return
	}
	affected := []deletionDevice{}
	for _, d := range devices {
		a, e := s.Store.Assignment(d.ID)
		if e != nil {
			result(w, nil, e)
			return
		}
		found := false
		for _, id := range a.NodeIDs {
			if id == n.ID {
				found = true
			}
		}
		if !found {
			continue
		}
		candidates := []model.Node{}
		for _, id := range a.NodeIDs {
			if id == n.ID {
				continue
			}
			candidate, e := s.Store.Node(id)
			if e != nil {
				result(w, nil, e)
				return
			}
			if candidate.Lifecycle != "deleted" && candidate.Lifecycle != "retiring" {
				candidates = append(candidates, candidate)
			}
		}
		affected = append(affected, deletionDevice{ID: d.ID, Name: d.Name, Assignment: a, Candidates: candidates})
	}
	reply(w, 200, map[string]any{"node": n, "devices": affected, "subscriptionTemplates": templates})
}
func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	var opts model.NodeDeleteOptions
	if !readJSON(w, r, &opts) {
		return
	}
	n, err := s.Store.DeleteNode(r.PathValue("id"), opts)
	if err != nil {
		var conflict *store.NodeSubscriptionConflict
		if errors.As(err, &conflict) {
			reply(w, http.StatusConflict, map[string]any{"error": conflict.Error(), "code": "NODE_REFERENCED_BY_SUBSCRIPTIONS", "subscriptionTemplates": conflict.Templates})
			return
		}
		managementError(w, err)
		return
	}
	s.signal()
	reply(w, 202, n)
}
func (s *Server) retireNode(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if len(b.Password) < 1 || len(b.Password) > 1024 {
		fail(w, 400, "请填写 SSH 密码")
		return
	}
	n, err := s.Store.QueueRetirement(r.PathValue("id"), b.Password)
	if err != nil {
		managementError(w, err)
		return
	}
	s.signal()
	reply(w, 202, n)
}
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.DeleteDevice(r.PathValue("id"))
	if err != nil {
		managementError(w, err)
		return
	}
	s.signal()
	reply(w, 202, d)
}
