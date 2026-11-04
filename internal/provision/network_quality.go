package provision

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode"
)

//go:embed ipquality/* ipquality/ref/*
var qualityAssets embed.FS

//go:embed network_quality.py
var qualityProgram string

// QualityError contains only a predefined safe diagnostic, never shell output.
type QualityError struct{ Message string }

func (e *QualityError) Error() string { return e.Message }

// NetworkQuality runs an isolated diagnostic, never a VPN readiness decision.
func (r *Runner) NetworkQuality(ctx context.Context, in Input, pin PinFunc) (json.RawMessage, error) {
	ip := net.ParseIP(in.Node.PublicIP)
	if ip == nil || ip.To4() == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return nil, errors.New("网络质量检测需要节点的公网 IPv4")
	}
	if in.Node.HostKey == "" || in.Password == "" {
		return nil, errors.New("检测需要已验证的主机身份和本次 SSH 密码")
	}
	files := map[string]string{}
	err := fs.WalkDir(qualityAssets, "ipquality", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := qualityAssets.ReadFile(path)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(path, "ipquality/")] = base64.StdEncoding.EncodeToString(data)
		return nil
	})
	if err != nil {
		return nil, errors.New("内置检测资源不可用")
	}
	runID := secret()[:24]
	raw, _ := json.Marshal(map[string]any{"ip": ip.String(), "files": files, "runId": runID})
	program := "payload_b64=" + shellQuote(base64.StdEncoding.EncodeToString(raw)) + "\n" + qualityProgram
	ctx, cancel := context.WithTimeout(ctx, 350*time.Second)
	defer cancel()
	remote, err := r.connect(ctx, in, pin)
	if err != nil {
		return nil, err
	}
	defer remote.client.Close()
	out, err := remote.run(ctx, "python3 -", strings.NewReader(program))
	if ctx.Err() != nil {
		stopCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_, _ = remote.run(stopCtx, "systemctl stop "+shellQuote("hqip-quality-"+runID), nil)
		return nil, &QualityError{Message: "网络质量检测已取消或超时"}
	}
	if err != nil {
		return nil, errors.New("远端检测启动或执行失败，请检查 SSH、Python 3 和 systemd")
	}
	if len(out) > 256*1024 {
		return nil, errors.New("检测报告超过大小限制")
	}
	var result struct {
		Error  string          `json:"error"`
		Report json.RawMessage `json:"report"`
	}
	if json.Unmarshal([]byte(out), &result) != nil {
		return nil, errors.New("检测返回了无效报告")
	}
	if result.Error != "" {
		messages := map[string]string{"dependencies": "缺少检测依赖：需要 bash、curl、jq、dig、ip、timeout 和 systemd-run；安装后可重新检测", "source": "节点 IP 未绑定到本机，无法检测指定出口", "timeout": "网络质量检测超时，可稍后重新检测", "execution": "检测未生成报告，外部查询服务可能暂不可用", "invalid": "检测报告无效或出口 IP 不符", "sandbox": "无法创建受限检测进程", "platform": "系统缺少非特权 nobody 账户"}
		if message, ok := messages[result.Error]; ok {
			return nil, &QualityError{Message: message}
		}
		return nil, errors.New("网络质量检测失败")
	}
	return validateQualityReport(result.Report, ip.String())
}

func validateQualityReport(raw json.RawMessage, ip string) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 192*1024 {
		return nil, errors.New("检测报告大小无效")
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil, errors.New("检测报告格式无效")
	}
	var head struct {
		IP string `json:"IP"`
	}
	if json.Unmarshal(doc["Head"], &head) != nil || head.IP != ip {
		return nil, errors.New("检测报告出口 IP 与节点不一致")
	}
	for _, key := range []string{"Head", "Info", "Type", "Score", "Factor", "Media", "Mail"} {
		var value map[string]json.RawMessage
		if json.Unmarshal(doc[key], &value) != nil || value == nil {
			return nil, errors.New("检测报告缺少必要字段")
		}
	}
	// Keep upstream factual fields only; renderers must treat them as plain data.
	clean := map[string]json.RawMessage{}
	for _, key := range []string{"Head", "Info", "Type", "Score", "Factor", "Media", "Mail"} {
		clean[key] = doc[key]
	}
	normalized, err := json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	var values any
	if err = json.Unmarshal(normalized, &values); err != nil {
		return nil, err
	}
	return json.Marshal(cleanQualityStrings(values))
}

var qualityANSI = regexp.MustCompile("\\x1b(?:\\[[0-?]*[ -/]*[@-~]|[0-9;]*[A-Za-z])")

func cleanQualityStrings(value any) any {
	switch v := value.(type) {
	case string:
		return strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, qualityANSI.ReplaceAllString(v, ""))
	case []any:
		for i := range v {
			v[i] = cleanQualityStrings(v[i])
		}
		return v
	case map[string]any:
		for k, x := range v {
			v[k] = cleanQualityStrings(x)
		}
		return v
	default:
		return value
	}
}
