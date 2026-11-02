package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.Store.Nodes()
	if err != nil {
		result(w, nil, err)
		return
	}
	devices, err := s.Store.Devices()
	if err != nil {
		result(w, nil, err)
		return
	}
	traffic, err := s.Store.Traffic("", "", r.URL.Query().Get("period"))
	if err != nil {
		result(w, nil, err)
		return
	}
	reply(w, 200, map[string]any{"nodes": nodes, "devices": devices, "traffic": traffic, "alerts": s.Store.Alerts(nodes), "serverTime": s.Store.Now().UTC().Format(time.RFC3339)})
}
func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.Nodes()
	result(w, v, e)
}
func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.Devices()
	result(w, v, e)
}

type nodeForm struct {
	Name              string   `json:"name"`
	SSHHost           string   `json:"sshHost"`
	SSHPort           int      `json:"sshPort"`
	SSHUser           string   `json:"sshUser"`
	PublicIP          string   `json:"publicIp"`
	Domain            string   `json:"domain"`
	Port              int      `json:"port"`
	BandwidthMbps     *float64 `json:"bandwidthMbps"`
	MonthlyQuotaBytes *int64   `json:"monthlyQuotaBytes"`
	Password          string   `json:"password"`
}

var domainRE = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$`)
var userRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,31}$`)

func validName(name string) bool {
	return strings.TrimSpace(name) != "" && len([]rune(name)) <= 80 && !strings.ContainsAny(name, "\r\n\x00")
}
func validQuota(bandwidth *float64, quota *int64) bool {
	return (bandwidth == nil || (*bandwidth > 0 && *bandwidth <= 1e9)) && (quota == nil || (*quota > 0 && *quota <= 1e18))
}
func (f *nodeForm) validate() error {
	f.Name = strings.TrimSpace(f.Name)
	f.SSHHost = strings.TrimSpace(f.SSHHost)
	f.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(f.Domain), "."))
	if f.SSHPort == 0 {
		f.SSHPort = 22
	}
	if f.SSHUser == "" {
		f.SSHUser = "root"
	}
	if f.Port == 0 {
		f.Port = 443
	}
	if !validName(f.Name) {
		return errors.New("节点名称应为 1–80 个字符")
	}
	if len(f.SSHHost) > 253 || (net.ParseIP(f.SSHHost) == nil && !domainRE.MatchString(f.SSHHost)) {
		return errors.New("SSH 地址必须为 IP 或完整域名")
	}
	if !userRE.MatchString(f.SSHUser) {
		return errors.New("SSH 用户名格式无效")
	}
	if f.SSHPort < 1 || f.SSHPort > 65535 || f.Port < 1 || f.Port > 65535 {
		return errors.New("端口应为 1–65535")
	}
	ip := net.ParseIP(f.PublicIP)
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return errors.New("请填写服务器的公网 IPv4 地址")
	}
	if len(f.Domain) > 253 || !domainRE.MatchString(f.Domain) {
		return errors.New("请填写已解析到服务器的节点域名")
	}
	if len(f.Password) < 1 || len(f.Password) > 1024 {
		return errors.New("请填写 SSH 密码（最多 1024 字符）")
	}
	if !validQuota(f.BandwidthMbps, f.MonthlyQuotaBytes) {
		return errors.New("带宽和流量额度必须为正数；无限请填写 null")
	}
	return nil
}
func (s *Server) createNode(w http.ResponseWriter, r *http.Request) {
	var f nodeForm
	if !readJSON(w, r, &f) {
		return
	}
	if err := f.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	n, err := s.Store.CreateNode(model.Node{Name: f.Name, SSHHost: f.SSHHost, SSHPort: f.SSHPort, SSHUser: f.SSHUser, PublicIP: f.PublicIP, Domain: f.Domain, Port: f.Port, BandwidthMbps: f.BandwidthMbps, MonthlyQuotaBytes: f.MonthlyQuotaBytes}, f.Password)
	if err != nil {
		fail(w, 409, "创建节点失败，域名或服务器端口可能已存在")
		return
	}
	s.signal()
	reply(w, 202, n)
}
func (s *Server) nodeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := s.Store.Node(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	events, err := s.Store.Events(id, 0)
	if err != nil {
		result(w, nil, err)
		return
	}
	traffic, err := s.Store.Traffic(id, "", r.URL.Query().Get("period"))
	if err != nil {
		result(w, nil, err)
		return
	}
	devices, err := s.Store.NodeDevices(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	reply(w, 200, map[string]any{"node": n, "events": events, "traffic": traffic, "devices": devices})
}
func (s *Server) deviceDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.Store.Device(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	traffic, err := s.Store.Traffic("", id, r.URL.Query().Get("period"))
	if err != nil {
		result(w, nil, err)
		return
	}
	nodes, err := s.Store.Nodes()
	if err != nil {
		result(w, nil, err)
		return
	}
	for i := range nodes {
		var mu, md, tu, td int64
		err = s.Store.DB.QueryRow("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0) FROM daily WHERE node_id=? AND device_id=? AND start>=?", nodes[i].ID, id, store.MonthStart(s.Store.Now())).Scan(&mu, &md)
		if err != nil {
			result(w, nil, err)
			return
		}
		err = s.Store.DB.QueryRow("SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0) FROM lifetime WHERE node_id=? AND device_id=?", nodes[i].ID, id).Scan(&tu, &td)
		if err != nil {
			result(w, nil, err)
			return
		}
		nodes[i].MonthUpload = mu
		nodes[i].MonthDownload = md
		nodes[i].TotalUpload = tu
		nodes[i].TotalDownload = td
	}
	assignment, err := s.Store.Assignment(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	account, err := s.Store.ClientAccount(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	sessions, err := s.Store.ClientSessions(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	removals, err := s.Store.Removals(id)
	if err != nil {
		result(w, nil, err)
		return
	}
	reply(w, 200, map[string]any{"device": d, "traffic": traffic, "nodes": nodes, "assignment": assignment, "clientAccount": account, "sessions": sessions, "removals": removals})
}
func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.Node(r.PathValue("id"))
	if err != nil {
		result(w, nil, err)
		return
	}
	var fields map[string]json.RawMessage
	if !readJSON(w, r, &fields) {
		return
	}
	for key, value := range fields {
		switch key {
		case "name":
			if json.Unmarshal(value, &n.Name) != nil || !validName(n.Name) {
				fail(w, 400, "节点名称应为 1–80 个字符")
				return
			}
			n.Name = strings.TrimSpace(n.Name)
		case "bandwidthMbps":
			if json.Unmarshal(value, &n.BandwidthMbps) != nil {
				fail(w, 400, "带宽格式无效")
				return
			}
		case "monthlyQuotaBytes":
			if json.Unmarshal(value, &n.MonthlyQuotaBytes) != nil {
				fail(w, 400, "流量额度格式无效")
				return
			}
		default:
			fail(w, 400, "只能修改名称、带宽和流量额度")
			return
		}
	}
	if !validQuota(n.BandwidthMbps, n.MonthlyQuotaBytes) {
		fail(w, 400, "带宽和流量额度必须为正数；无限请填写 null")
		return
	}
	if err = s.Store.SaveNode(n); err != nil {
		result(w, nil, err)
		return
	}
	s.Store.Audit("node.update", n.ID)
	n, err = s.Store.Node(n.ID)
	result(w, n, err)
}
func (s *Server) retryNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Password) < 1 || len(body.Password) > 1024 {
		fail(w, 400, "请重新填写 SSH 密码")
		return
	}
	n, err := s.Store.Queue(r.PathValue("id"), "install", body.Password)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	s.signal()
	reply(w, 202, n)
}
func (s *Server) verifyNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.Queue(r.PathValue("id"), "verify", "")
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	s.signal()
	reply(w, 202, n)
}
func (s *Server) externalVerification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExitIP string `json:"exitIp"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	n, err := s.Store.Node(r.PathValue("id"))
	if err != nil {
		result(w, nil, err)
		return
	}
	if net.ParseIP(body.ExitIP) == nil || body.ExitIP != n.PublicIP {
		fail(w, 400, "外部验证出口 IP 与节点公网 IP 不一致")
		return
	}
	if n.Status != "awaiting_external" && n.Status != "ready" {
		fail(w, 409, "请先完成节点安装与控制端连通性验证")
		return
	}
	n.Status = "ready"
	n.Message = "外部 Mihomo 验证通过"
	if err = s.Store.ExternalVerified(n.ID); err != nil {
		fail(w, 409, err.Error())
		return
	}
	_ = s.Store.Event(n.ID, "ready", n.Message)
	s.Store.Audit("node.external_verified", n.ID)
	reply(w, 200, n)
}
func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if !validName(b.Name) {
		fail(w, 400, "设备名称应为 1–80 个字符")
		return
	}
	d, err := s.Store.CreateDevice(b.Name)
	if err != nil {
		result(w, nil, err)
		return
	}
	reply(w, 201, d)
}
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if b.Name != nil {
		trimmed := strings.TrimSpace(*b.Name)
		b.Name = &trimmed
		if !validName(trimmed) {
			fail(w, 400, "设备名称应为 1–80 个字符")
			return
		}
	}
	d, err := s.Store.UpdateDevice(r.PathValue("id"), b.Name, b.Enabled, false)
	result(w, d, err)
}
func (s *Server) rotateDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.UpdateDevice(r.PathValue("id"), nil, nil, true)
	result(w, d, err)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.Node(id); err != nil {
		result(w, nil, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "当前连接不支持实时进度")
		return
	}
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if after < 0 {
		after = 0
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	send := func() bool {
		events, err := s.Store.Events(id, after)
		if err != nil {
			return false
		}
		for _, event := range events {
			raw, _ := json.Marshal(event)
			if _, err = fmt.Fprintf(w, "id: %d\nevent: progress\ndata: %s\n\n", event.ID, raw); err != nil {
				return false
			}
			after = event.ID
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
			if !send() {
				return
			}
		case <-heartbeat.C:
			if _, err := s.Store.Session(cookieToken(r)); err != nil {
				return
			}
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
func (s *Server) agentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		fail(w, 401, "Agent 身份验证失败")
		return "", false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	if len(token) != 43 {
		fail(w, 401, "Agent 身份验证失败")
		return "", false
	}
	id, err := s.Store.ResolveAgent(token)
	if err != nil {
		fail(w, 401, "Agent 身份验证失败")
		return "", false
	}
	return id, true
}
func (s *Server) agentConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := s.agentID(w, r)
	if !ok {
		return
	}
	d, err := s.Store.Desired(id)
	result(w, d, err)
}
func (s *Server) agentReport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.agentID(w, r)
	if !ok {
		return
	}
	var report model.AgentReport
	if !readJSON(w, r, &report) {
		return
	}
	if err := s.Store.Report(id, report); err != nil {
		fail(w, 400, "流量上报失败：记录数量、时间或计数不合法")
		return
	}
	d, err := s.Store.Desired(id)
	result(w, d, err)
}
