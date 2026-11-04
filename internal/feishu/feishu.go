// Package feishu sends signed custom-bot notifications. It deliberately exposes
// no arbitrary webhook transport, redirects, environment proxies or raw errors.
package feishu

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var hookPath = regexp.MustCompile(`^/open-apis/bot/v2/hook/[A-Za-z0-9_-]{16,128}$`)
var detailPath = regexp.MustCompile(`^/admin/(notifications|domains|nodes/[A-Za-z0-9_-]+|servers/[A-Za-z0-9_-]+|users/[A-Za-z0-9_-]+)$`)

func ValidateWebhook(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || (u.Host != "open.feishu.cn" && u.Host != "open.feishu.cn:443") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || !hookPath.MatchString(u.Path) || strings.ContainsAny(raw, "\r\n\t ") {
		return errors.New("请填写标准飞书 HTTPS 机器人 Webhook 地址")
	}
	return nil
}

// Sign follows the custom-bot protocol: timestamp + newline + secret is the
// HMAC key and the signed message is empty (not the JSON request body).
func Sign(timestamp, secret string) string {
	m := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

type Event struct {
	ID, Level, Title, Message, DetailPath string
	CreatedAt                             time.Time
}

type Message struct {
	BaseURL string
	Events  []Event
}

type Result struct {
	Success   bool
	Retryable bool
	Code      int
	Error     string
}

type Sender struct {
	client *http.Client
	now    func() time.Time
}

func New() *Sender {
	transport := &http.Transport{
		Proxy: nil, DialContext: publicDial,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second,
		MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Minute,
	}
	return &Sender{client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, now: time.Now}
}

var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, prefix := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/32", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "fec0::/10"} {
		out = append(out, netip.MustParsePrefix(prefix))
	}
	return out
}()

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "open.feishu.cn" || port != "443" {
		return nil, errors.New("机器人目标地址无效")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("飞书域名解析失败")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errors.New("飞书目标必须是公网地址")
		}
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	for _, ip := range ips {
		conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("无法连接飞书服务器")
}

func plain(s string, limit int) string {
	r := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, s))
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return string(r)
}

func link(base, path string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ""
	}
	if !detailPath.MatchString(path) {
		path = "/admin/notifications"
	}
	u.Path = path
	return u.String()
}

func payload(at time.Time, secret string, message Message) ([]byte, error) {
	if len(message.Events) == 0 || len(message.Events) > 8 {
		return nil, errors.New("通知批次大小无效")
	}
	zone := time.FixedZone("北京时间", 8*60*60)
	elements := []any{}
	color := "blue"
	for _, e := range message.Events {
		if e.Level == "danger" {
			color = "red"
		} else if e.Level == "warning" && color != "red" {
			color = "orange"
		}
		level := map[string]string{"danger": "严重", "error": "严重", "warning": "预警", "info": "提示"}[e.Level]
		if level == "" {
			level = "提示"
		}
		text := "【" + level + "】" + plain(e.Title, 100) + "\n" + plain(e.Message, 400) + "\n北京时间：" + e.CreatedAt.In(zone).Format("2006-01-02 15:04:05") + "\n事件编号：" + plain(e.ID, 80)
		elements = append(elements, map[string]any{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": text}})
		if target := link(message.BaseURL, e.DetailPath); target != "" {
			elements = append(elements, map[string]any{"tag": "action", "actions": []any{map[string]any{"tag": "button", "text": map[string]string{"tag": "plain_text", "content": "查看后台详情"}, "type": "default", "url": target}}})
		}
	}
	timestamp := strconv.FormatInt(at.Unix(), 10)
	body := map[string]any{"timestamp": timestamp, "sign": Sign(timestamp, secret), "msg_type": "interactive", "card": map[string]any{"header": map[string]any{"template": color, "title": map[string]string{"tag": "plain_text", "content": "HQIP · 系统通知"}}, "elements": elements}}
	data, err := json.Marshal(body)
	if len(data) > 20*1024 {
		return nil, errors.New("通知内容超过飞书大小限制")
	}
	return data, err
}

func (s *Sender) Send(ctx context.Context, webhook, secret string, message Message) Result {
	if err := ValidateWebhook(webhook); err != nil {
		return Result{Error: err.Error()}
	}
	if secret == "" {
		return Result{Error: "尚未配置签名密钥"}
	}
	body, err := payload(s.now(), secret, message)
	if err != nil {
		return Result{Error: "通知内容格式无效或过长"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return Result{Error: "机器人请求无效"}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{Retryable: true, Error: "无法连接飞书或请求超时；请检查网络，超时重试可能产生重复通知"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{Retryable: resp.StatusCode == 429 || resp.StatusCode >= 500, Error: fmt.Sprintf("飞书 HTTP 响应异常（%d）", resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024+1))
	if err != nil {
		return Result{Retryable: true, Error: "读取飞书响应失败，重试可能产生重复通知"}
	}
	var response struct {
		Code *int `json:"code"`
	}
	if len(data) > 32*1024 || json.Unmarshal(data, &response) != nil || response.Code == nil {
		return Result{Error: "飞书响应格式无效，未确认接收"}
	}
	if *response.Code == 0 {
		return Result{Success: true}
	}
	result := Result{Code: *response.Code, Error: fmt.Sprintf("飞书拒绝通知（错误码 %d）", *response.Code)}
	switch *response.Code {
	case 11232:
		result.Retryable = true
		result.Error = "飞书发送频率受限，稍后自动重试（11232）"
	case 19021:
		result.Error = "飞书签名校验失败，请检查签名密钥与服务器时间（19021）"
	case 19022:
		result.Error = "飞书 IP 白名单拒绝，请允许控制服务器出口 IP（19022）"
	case 19024:
		result.Error = "飞书关键词校验失败，请允许 HQIP 关键词（19024）"
	case 9499:
		result.Error = "飞书请求格式校验失败（9499）"
	}
	return result
}
