// Package payment implements the FM wire protocol. It never decides which
// subscription a payment buys; all authorization belongs to the order ledger.
package payment

import (
	"context"
	"crypto/md5"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
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
)

type Config struct {
	APIBaseURL         string
	MerchantNum        string
	Secret             string
	PayType            string
	PayDurationMinutes int
}

type CreateRequest struct {
	Config      Config
	OrderID     string
	OrderNo     string
	AmountCents int64
	Subject     string
	NotifyURL   string
}

type CreatedOrder struct {
	PlatformOrderNo string
	PayURL          string
}

type QueryResult struct {
	PlatformOrderNo   string
	OrderNo           string
	MerchantNum       string
	AmountCents       int64
	ActualAmountCents int64
	State             string
	PayType           string
	ReportedPaidAt    string
}

// ProviderError contains only a fixed, safe code. Raw responses, URLs with
// signatures, transport errors and merchant credentials are never returned.
type ProviderError struct {
	Code      string
	Uncertain bool
}

func (e *ProviderError) Error() string {
	switch e.Code {
	case "rejected":
		return "支付服务拒绝创建订单，请检查商户配置"
	case "invalid_response":
		return "支付服务返回异常，结果待确认，请勿重复付款"
	case "query_failed":
		return "支付查询失败，请确认商户已开通查单权限"
	default:
		return "支付服务暂不可用，结果待确认，请勿重复付款"
	}
}

type Client struct {
	// Transport is an explicit test seam. Production leaves it nil to use the
	// pinned-public-address transport below (no environment proxies).
	Transport    http.RoundTripper
	ReturnOrigin string
}

var merchantPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var orderPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
var decimalPattern = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,2})?$`)
var privateNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

func publicAddress(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, prefix := range privateNetworks {
		if prefix.Contains(a) {
			return false
		}
	}
	return true
}

// ValidateURL performs syntax/obvious-target checks. DNS is re-resolved and
// pinned to a public address by DialContext, preventing DNS rebinding.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || strings.ContainsAny(raw, "\r\n\x00") {
		return errors.New("支付地址必须为有效的公网 HTTPS 地址")
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") || !strings.Contains(h, ".") && !strings.Contains(h, ":") {
		return errors.New("支付地址不能指向本地网络")
	}
	if a, e := netip.ParseAddr(h); e == nil && !publicAddress(a) {
		return errors.New("支付地址不能指向本地网络")
	}
	return nil
}

func NormalizeBaseURL(raw string) (string, error) {
	if err := ValidateURL(raw); err != nil {
		return "", err
	}
	u, _ := url.Parse(raw)
	if u.RawQuery != "" || u.ForceQuery || strings.Contains(u.Path, "..") {
		return "", errors.New("支付接口根地址不能含查询参数或父目录")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func ValidateConfig(c Config) error {
	if _, err := NormalizeBaseURL(c.APIBaseURL); err != nil {
		return err
	}
	if !merchantPattern.MatchString(c.MerchantNum) || len(c.Secret) < 8 || len(c.Secret) > 512 || strings.TrimSpace(c.Secret) != c.Secret || strings.ContainsAny(c.Secret, "\r\n\x00") {
		return errors.New("支付商户号或密钥格式无效")
	}
	if c.PayType != "aloop" && c.PayType != "alipay" {
		return errors.New("请选择支持的支付宝支付通道")
	}
	if c.PayDurationMinutes < 1 || c.PayDurationMinutes > 15 {
		return errors.New("支付有效期须为 1 至 15 分钟")
	}
	return nil
}

var safeTransport = &http.Transport{
	TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	Proxy:           nil, ForceAttemptHTTP2: true, MaxIdleConns: 20, MaxIdleConnsPerHost: 4,
	IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
	ResponseHeaderTimeout: 15 * time.Second,
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || port != "443" {
			return nil, errors.New("invalid payment endpoint")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("payment DNS unavailable")
		}
		for _, ip := range ips {
			if !publicAddress(ip) {
				return nil, errors.New("payment endpoint is not public")
			}
		}
		d := net.Dialer{Timeout: 5 * time.Second}
		for _, ip := range ips {
			conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("payment endpoint unavailable")
	},
}

func digest(text string) string       { sum := md5.Sum([]byte(text)); return hex.EncodeToString(sum[:]) }
func FormatAmount(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }
func ParseAmount(s string) (int64, error) {
	if !decimalPattern.MatchString(s) {
		return 0, errors.New("支付金额格式无效")
	}
	a, b, _ := strings.Cut(s, ".")
	if len(b) == 0 {
		b = "00"
	} else if len(b) == 1 {
		b += "0"
	}
	y, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return 0, errors.New("支付金额格式无效")
	}
	f, _ := strconv.ParseInt(b, 10, 64)
	return y*100 + f, nil
}

func (c *Client) post(ctx context.Context, base, path string, form url.Values, out any) error {
	if _, err := NormalizeBaseURL(base); err != nil {
		return &ProviderError{Code: "invalid_response", Uncertain: false}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return &ProviderError{Code: "invalid_response", Uncertain: false}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	transport := http.RoundTripper(safeTransport)
	if c != nil && c.Transport != nil {
		transport = c.Transport
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return &ProviderError{Code: "unavailable", Uncertain: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &ProviderError{Code: "unavailable", Uncertain: true}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 || json.Unmarshal(body, out) != nil {
		return &ProviderError{Code: "invalid_response", Uncertain: true}
	}
	return nil
}

func (c *Client) Create(ctx context.Context, in CreateRequest) (CreatedOrder, error) {
	var out CreatedOrder
	if err := ValidateConfig(in.Config); err != nil {
		return out, &ProviderError{Code: "invalid_response"}
	}
	if !orderPattern.MatchString(in.OrderNo) || in.AmountCents < 100 || in.AmountCents > 100000000 || len(in.NotifyURL) > 200 || ValidateURL(in.NotifyURL) != nil {
		return out, &ProviderError{Code: "invalid_response"}
	}
	amount := FormatAmount(in.AmountCents)
	sign := digest(in.Config.MerchantNum + in.OrderNo + amount + in.NotifyURL + in.Config.Secret)
	form := url.Values{"merchantNum": {in.Config.MerchantNum}, "orderNo": {in.OrderNo}, "amount": {amount}, "notifyUrl": {in.NotifyURL}, "payType": {in.Config.PayType}, "sign": {sign}, "returnType": {"json"}, "apiMode": {"post_form"}, "payDuration": {strconv.Itoa(in.Config.PayDurationMinutes)}, "subject": {in.Subject}, "body": {"HQIP " + in.Subject}}
	if c != nil && c.ReturnOrigin != "" {
		u, e := url.Parse(c.ReturnOrigin)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			return out, &ProviderError{Code: "invalid_response"}
		}
		form.Set("returnUrl", strings.TrimRight(c.ReturnOrigin, "/")+"/orders/"+url.PathEscape(in.OrderID))
	}
	var response struct {
		Success *bool `json:"success"`
		Code    int   `json:"code"`
		Data    *struct {
			ID     string `json:"id"`
			PayURL string `json:"payUrl"`
		} `json:"data"`
	}
	if err := c.post(ctx, in.Config.APIBaseURL, "/startOrder", form, &response); err != nil {
		return out, err
	}
	if response.Success == nil || response.Code == 0 {
		return out, &ProviderError{Code: "invalid_response", Uncertain: true}
	}
	if !*response.Success {
		return out, &ProviderError{Code: "rejected"}
	}
	if response.Code != 200 || response.Data == nil || response.Data.ID == "" || len(response.Data.ID) > 128 || ValidateURL(response.Data.PayURL) != nil {
		return out, &ProviderError{Code: "invalid_response", Uncertain: true}
	}
	out.PlatformOrderNo = response.Data.ID
	out.PayURL = response.Data.PayURL
	return out, nil
}

// NotifyProof deliberately excludes unsigned metadata from authorization.
type NotifyProof struct {
	MerchantNum, OrderNo, PlatformOrderNo, PayType, ReportedPaidAt string
	AmountCents, ActualAmountCents                                 int64
}

func VerifyNotify(values url.Values, secret string) (NotifyProof, error) {
	var out NotifyProof
	for k, v := range values {
		if len(v) != 1 || len(v[0]) > 2048 || strings.ContainsAny(v[0], "\r\n\x00") {
			return out, errors.New("无效支付通知")
		}
		if len(k) > 80 {
			return out, errors.New("无效支付通知")
		}
	}
	for _, k := range []string{"merchantNum", "orderNo", "platformOrderNo", "amount", "actualPayAmount", "state", "type", "sign", "payTime"} {
		if values.Get(k) == "" {
			return out, errors.New("无效支付通知")
		}
	}
	if values.Get("state") != "1" || !merchantPattern.MatchString(values.Get("merchantNum")) || !orderPattern.MatchString(values.Get("orderNo")) || len(values.Get("platformOrderNo")) > 128 || len(values.Get("type")) > 80 || len(values.Get("payTime")) > 128 {
		return out, errors.New("无效支付通知")
	}
	expected := digest("1" + values.Get("merchantNum") + values.Get("orderNo") + values.Get("amount") + secret)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(values.Get("sign"))) != 1 {
		return out, errors.New("支付通知签名无效")
	}
	amount, e := ParseAmount(values.Get("amount"))
	if e != nil || amount < 100 || amount > 100000000 {
		return out, errors.New("无效支付金额")
	}
	actual, e := ParseAmount(values.Get("actualPayAmount"))
	if e != nil || actual <= 0 {
		return out, errors.New("无效支付金额")
	}
	out = NotifyProof{MerchantNum: values.Get("merchantNum"), OrderNo: values.Get("orderNo"), PlatformOrderNo: values.Get("platformOrderNo"), PayType: values.Get("type"), ReportedPaidAt: values.Get("payTime"), AmountCents: amount, ActualAmountCents: actual}
	return out, nil
}

func (c *Client) Query(ctx context.Context, config Config, orderNo string) (QueryResult, error) {
	var out QueryResult
	if ValidateConfig(config) != nil || !orderPattern.MatchString(orderNo) {
		return out, &ProviderError{Code: "query_failed"}
	}
	form := url.Values{"merchantNum": {config.MerchantNum}, "orderNo": {orderNo}, "sign": {digest(config.MerchantNum + orderNo + config.Secret)}}
	var r struct {
		Success bool `json:"success"`
		Code    int  `json:"code"`
		Data    *struct {
			OrderID     string `json:"orderId"`
			OrderNo     string `json:"orderNo"`
			MerchantNum string `json:"merchantNum"`
			Amount      string `json:"amount"`
			Actual      string `json:"tradeMoney"`
			State       string `json:"orderState"`
			PayType     string `json:"payType"`
			PaidAt      string `json:"payTime"`
		} `json:"data"`
	}
	if err := c.post(ctx, config.APIBaseURL, "/queryOutOrder", form, &r); err != nil {
		return out, &ProviderError{Code: "query_failed"}
	}
	if !r.Success || r.Code != 200 || r.Data == nil {
		return out, &ProviderError{Code: "query_failed"}
	}
	amount, e := ParseAmount(r.Data.Amount)
	if e != nil || amount < 100 || r.Data.OrderID == "" || len(r.Data.OrderID) > 128 {
		return out, &ProviderError{Code: "query_failed"}
	}
	actual := int64(0)
	if r.Data.Actual != "" {
		actual, e = ParseAmount(r.Data.Actual)
		if e != nil {
			return out, &ProviderError{Code: "query_failed"}
		}
	}
	return QueryResult{PlatformOrderNo: r.Data.OrderID, OrderNo: r.Data.OrderNo, MerchantNum: r.Data.MerchantNum, AmountCents: amount, ActualAmountCents: actual, State: r.Data.State, PayType: r.Data.PayType, ReportedPaidAt: r.Data.PaidAt}, nil
}
