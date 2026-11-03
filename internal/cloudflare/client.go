// Package cloudflare contains the deliberately small DNS API surface used by HQIP.
// It never exposes arbitrary API URLs or forwards response bodies into logs.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const apiURL = "https://api.cloudflare.com/client/v4"

var identifier = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
var ErrConflict = errors.New("DNS 名称已有其他记录，请更换名称")
var ErrChanged = errors.New("DNS 记录已被外部修改，需人工处理")
var ErrNotFound = errors.New("Cloudflare DNS 记录不存在")

type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}
type Record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}
type Client struct {
	token string
	http  *http.Client
}

func New(token string) *Client { return NewWithTransport(token, http.DefaultTransport) }

// NewWithTransport is for deterministic tests. Requests still use the fixed HTTPS API origin.
func NewWithTransport(token string, transport http.RoundTripper) *Client {
	return &Client{token: token, http: &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func ValidID(id string) bool { return identifier.MatchString(id) }
func ValidateToken(token string) error {
	if len(token) < 20 || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("Cloudflare Token 格式无效")
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, path string, body, result any) error {
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		in = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL+path, in)
	if err != nil {
		return errors.New("Cloudflare 请求无效")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Cloudflare 网络请求失败")
	}
	defer resp.Body.Close()
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Errors  []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return errors.New("Cloudflare 返回无效响应")
	}
	if resp.StatusCode == 404 {
		return ErrNotFound
	}
	if resp.StatusCode == 429 {
		return errors.New("Cloudflare 请求受限，请稍后重试")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Success {
		code := 0
		if len(envelope.Errors) > 0 {
			code = envelope.Errors[0].Code
		}
		return fmt.Errorf("Cloudflare 操作失败（HTTP %d，代码 %d）；请检查 Token 的 Zone:Read 和 DNS:Edit 权限", resp.StatusCode, code)
	}
	if result != nil {
		if err = json.Unmarshal(envelope.Result, result); err != nil {
			return errors.New("Cloudflare 响应结构无效")
		}
	}
	return nil
}
func (c *Client) Verify(ctx context.Context) error {
	var result struct {
		Status string `json:"status"`
	}
	if err := c.request(ctx, "GET", "/user/tokens/verify", nil, &result); err != nil {
		return err
	}
	if result.Status != "active" {
		return errors.New("Cloudflare Token 未启用")
	}
	return nil
}
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	all := []Zone{}
	for page := 1; page <= 100; page++ {
		var zones []Zone
		if err := c.request(ctx, "GET", fmt.Sprintf("/zones?per_page=50&page=%d", page), nil, &zones); err != nil {
			return nil, err
		}
		all = append(all, zones...)
		if len(zones) < 50 {
			return all, nil
		}
	}
	return nil, errors.New("Cloudflare 域名数量过多，请缩小 Token 授权范围")
}
func (c *Client) Zone(ctx context.Context, id string) (Zone, error) {
	var z Zone
	if !ValidID(id) {
		return z, errors.New("Cloudflare Zone ID 无效")
	}
	err := c.request(ctx, "GET", "/zones/"+id, nil, &z)
	if err == nil && (z.ID != id || z.Status != "active") {
		err = errors.New("Cloudflare Zone 尚未启用")
	}
	return z, err
}
func (c *Client) Records(ctx context.Context, zone, name string) ([]Record, error) {
	if !ValidID(zone) {
		return nil, errors.New("Cloudflare Zone ID 无效")
	}
	records := []Record{}
	for page := 1; page <= 20; page++ {
		var part []Record
		err := c.request(ctx, "GET", "/zones/"+zone+"/dns_records?name="+url.QueryEscape(name)+fmt.Sprintf("&per_page=100&page=%d", page), nil, &part)
		if err != nil {
			return nil, err
		}
		records = append(records, part...)
		if len(part) < 100 {
			return records, nil
		}
	}
	return nil, errors.New("DNS 同名记录过多")
}
func SameRecord(a, b Record) bool {
	return a.Type == b.Type && strings.EqualFold(strings.TrimSuffix(a.Name, "."), strings.TrimSuffix(b.Name, ".")) && a.Content == b.Content && a.Proxied == b.Proxied && a.Comment == b.Comment
}

// EnsureRecord recovers an interrupted POST by its durable random ownership comment.
// For A records every other same-name record is a collision; TXT records coexist.
func (c *Client) EnsureRecord(ctx context.Context, zone string, want Record) (Record, error) {
	records, err := c.Records(ctx, zone, want.Name)
	if err != nil {
		return Record{}, err
	}
	var owned *Record
	for _, r := range records {
		if SameRecord(r, want) {
			if want.ID != "" && want.ID != r.ID {
				return Record{}, ErrChanged
			}
			if owned != nil {
				return Record{}, ErrChanged
			}
			copy := r
			owned = &copy
			continue
		}
		if r.Comment == want.Comment {
			return Record{}, ErrChanged
		}
		if want.Type == "A" {
			return Record{}, ErrConflict
		}
	}
	if owned != nil {
		return *owned, nil
	}
	if want.ID != "" {
		// Once creation was acknowledged, a missing record is an external
		// change, not an interrupted POST. Do not recreate it behind the owner.
		return Record{}, ErrChanged
	}
	var created Record
	if err = c.request(ctx, "POST", "/zones/"+zone+"/dns_records", want, &created); err != nil {
		return created, err
	}
	if !ValidID(created.ID) || !SameRecord(created, want) {
		return Record{}, ErrChanged
	}
	return created, nil
}
func (c *Client) DeleteOwned(ctx context.Context, zone string, want Record) error {
	if !ValidID(zone) || !ValidID(want.ID) {
		return errors.New("DNS 记录标识无效")
	}
	var actual Record
	err := c.request(ctx, "GET", "/zones/"+zone+"/dns_records/"+want.ID, nil, &actual)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if actual.ID != want.ID || !SameRecord(actual, want) {
		return ErrChanged
	}
	return c.request(ctx, "DELETE", "/zones/"+zone+"/dns_records/"+want.ID, nil, nil)
}

// WaitPropagation checks every authoritative name server, rather than trusting a
// local recursive cache. A record must resolve exclusively to the selected IP.
func WaitPropagation(ctx context.Context, zone, name, kind, value string) error {
	for {
		nameservers, err := net.DefaultResolver.LookupNS(ctx, zone)
		ready := err == nil && len(nameservers) > 0
		for _, ns := range nameservers {
			server := strings.TrimSuffix(ns.Host, ".")
			resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(server, "53"))
			}}
			checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			if kind == "TXT" {
				values, e := resolver.LookupTXT(checkCtx, name)
				ok := false
				for _, v := range values {
					if v == value {
						ok = true
					}
				}
				ready = ready && e == nil && ok
			} else {
				values, e := resolver.LookupIP(checkCtx, "ip4", name)
				ok := e == nil && len(values) > 0
				for _, v := range values {
					if v.String() != value {
						ok = false
					}
				}
				ready = ready && ok
			}
			cancel()
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("DNS 权威解析尚未生效，请稍后重试")
		case <-time.After(3 * time.Second):
		}
	}
}
