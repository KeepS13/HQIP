package control

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/KeepS13/HQIP/internal/model"
	"gopkg.in/yaml.v3"
)

func (s *Server) subscription(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if len(token) != 43 {
		fail(w, 404, "订阅不存在或已撤销")
		return
	}
	device, err := s.Store.SubscriptionDevice(token)
	if err != nil {
		fail(w, 404, "订阅不存在或已撤销")
		return
	}
	nodes, err := s.Store.AssignedNodes(device.ID, true)
	if err != nil {
		fail(w, 503, "暂时无法生成订阅")
		return
	}
	ready := []model.Node{}
	for _, n := range nodes {
		if n.Status == "ready" {
			ready = append(ready, n)
		}
	}
	if len(ready) == 0 {
		w.Header().Set("Retry-After", "15")
		fail(w, 503, "尚无通过验证的节点，请等待部署和外部验证完成后更新订阅")
		return
	}
	data, err := s.clashConfig(device, ready)
	if err != nil {
		fail(w, 500, "生成订阅失败")
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=hqip.yaml")
	w.Header().Set("Profile-Update-Interval", "6")
	w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=%s; download=%s; total=0; expire=0", strconv.FormatInt(device.MonthUpload, 10), strconv.FormatInt(device.MonthDownload, 10)))
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

// Verification config is available only to the administrator, enabling an
// external test before the candidate is published in the ordinary subscription.
func (s *Server) verificationConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"deviceId"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	n, err := s.Store.Node(r.PathValue("id"))
	if err != nil {
		result(w, nil, err)
		return
	}
	if n.Status != "awaiting_external" && n.Status != "ready" {
		fail(w, 409, "节点尚未完成安装与控制端验证")
		return
	}
	d, err := s.Store.Device(body.DeviceID)
	if err != nil {
		result(w, nil, err)
		return
	}
	if !d.Enabled {
		fail(w, 400, "验证设备已停用")
		return
	}
	data, err := s.clashConfig(d, []model.Node{n})
	if err != nil {
		fail(w, 500, "生成验证配置失败")
		return
	}
	s.Store.Audit("node.verification_config", n.ID)
	reply(w, 200, map[string]string{"config": string(data)})
}

func (s *Server) clashConfig(device model.Device, nodes []model.Node) ([]byte, error) {
	proxies := []map[string]any{}
	names := []string{}
	for _, n := range nodes {
		password, err := s.Store.Credential(n.ID, device.ID)
		if err != nil {
			return nil, err
		}
		name := n.Name + " · " + n.PublicIP + " · " + n.ID[:6]
		names = append(names, name)
		proxies = append(proxies, map[string]any{"name": name, "type": "hysteria2", "server": n.PublicIP, "port": n.Port, "password": password, "sni": n.Domain, "skip-cert-verify": false})
	}
	selectNames := append(append([]string{}, names...), "自动测速")
	config := map[string]any{
		"mixed-port": 7890, "allow-lan": false, "mode": "rule", "log-level": "warning", "ipv6": false,
		"profile":      map[string]bool{"store-selected": true},
		"dns":          map[string]any{"enable": true, "listen": "127.0.0.1:1053", "ipv6": false, "enhanced-mode": "fake-ip", "fake-ip-range": "198.18.0.1/16", "fake-ip-filter": []string{"*.lan", "localhost.ptlogin2.qq.com"}, "default-nameserver": []string{"223.5.5.5", "1.1.1.1"}, "nameserver": []string{"https://1.1.1.1/dns-query", "https://dns.google/dns-query"}, "proxy-server-nameserver": []string{"223.5.5.5", "1.1.1.1"}},
		"proxies":      proxies,
		"proxy-groups": []map[string]any{{"name": "节点选择", "type": "select", "proxies": selectNames}, {"name": "自动测速", "type": "url-test", "proxies": names, "url": "https://www.gstatic.com/generate_204", "interval": 300, "tolerance": 50}},
		"rules":        []string{"DOMAIN-SUFFIX,local,DIRECT", "DOMAIN-SUFFIX,lan,DIRECT", "IP-CIDR,127.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,172.16.0.0/12,DIRECT,no-resolve", "IP-CIDR,192.168.0.0/16,DIRECT,no-resolve", "IP-CIDR,169.254.0.0/16,DIRECT,no-resolve", "IP-CIDR6,::1/128,DIRECT,no-resolve", "IP-CIDR6,fc00::/7,DIRECT,no-resolve", "IP-CIDR6,fe80::/10,DIRECT,no-resolve", "MATCH,节点选择"},
	}
	version, err := s.Store.Rules()
	if err != nil {
		return nil, err
	}
	routeRules, err := version.Bundle.ClashRules("节点选择", "DIRECT")
	if err != nil {
		return nil, err
	}
	config["rules"] = routeRules
	return yaml.Marshal(config)
}
