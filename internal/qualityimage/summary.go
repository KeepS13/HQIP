package qualityimage

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/width"
)

func legacySummary(raw json.RawMessage) (string, error) {
	var report map[string]any
	if json.Unmarshal(raw, &report) != nil || report == nil {
		return "", errors.New("invalid saved report")
	}
	head, info := object(report["Head"]), object(report["Info"])
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	heading := func(title string) {
		line("\n\x1b[1;36m%s\x1b[0m", title)
		line("────────────────────────────────────────────────────────────────────────────")
	}
	ip := head["IP"]
	if ip == nil {
		ip = head["IPv4"]
	}
	line("\x1b[1mIP 质量体检报告  %s\x1b[0m", value(ip))
	line("检测时间  %s", value(head["Time"]))
	line("检测来源  xykt/IPQuality · %s", value(head["Version"]))
	heading("01  基础信息")
	line("ASN       %s", value(info["ASN"]))
	line("网络组织  %s", value(info["Organization"]))
	line("地理位置  %s · %s", value(object(info["Region"])["Name"]), value(object(info["City"])["Name"]))
	line("注册地区  %s", value(object(info["RegisteredRegion"])["Name"]))
	line("IP 类型   %s", value(info["Type"]))
	line("时区      %s", value(info["TimeZone"]))
	line("经纬度    %s, %s", value(info["Latitude"]), value(info["Longitude"]))
	heading("02  用途与公司类型")
	types := object(report["Type"])
	usage, company := object(types["Usage"]), object(types["Company"])
	line("%s%s%s", pad("数据来源", 22), pad("使用类型", 26), "公司类型")
	providers := unionKeys(usage, company)
	if len(providers) == 0 {
		line("未知")
	}
	for _, k := range providers {
		line("%s%s%s", pad(k, 22), pad(value(usage[k]), 26), value(company[k]))
	}
	heading("03  信誉评分（各来源口径独立）")
	scores := object(report["Score"])
	if len(scores) == 0 {
		line("未知")
	}
	for _, k := range unionKeys(scores) {
		line("%s%s", pad(k, 22), value(scores[k]))
	}
	heading("04  流媒体与 AI 可访问性")
	media := object(report["Media"])
	line("%s%s%s%s", pad("服务", 22), pad("状态", 18), pad("地区", 12), "类型")
	if len(media) == 0 {
		line("未知")
	}
	names := map[string]string{"DisneyPlus": "Disney+", "Youtube": "YouTube", "AmazonPrimeVideo": "Prime Video"}
	for _, k := range unionKeys(media) {
		m := object(media[k])
		name := k
		if n := names[k]; n != "" {
			name = n
		}
		line("%s%s%s%s", pad(name, 22), pad(mediaStatus(m["Status"]), 18), pad(value(m["Region"]), 12), value(m["Type"]))
	}
	heading("05  风险标记（是 / 否 / 未知）")
	factors := object(report["Factor"])
	keys := []string{"CountryCode", "Proxy", "Tor", "VPN", "Server", "Abuser", "Robot"}
	groups := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		groups = append(groups, object(factors[k]))
	}
	line("%s%s%s%s%s%s%s%s", pad("数据来源", 18), pad("地区", 8), pad("代理", 8), pad("Tor", 8), pad("VPN", 8), pad("机房", 8), pad("滥用", 8), "机器人")
	providers = unionKeys(groups...)
	if len(providers) == 0 {
		line("未知")
	}
	for _, provider := range providers {
		var row strings.Builder
		row.WriteString(pad(provider, 18))
		for _, g := range groups {
			row.WriteString(pad(value(g[provider]), 8))
		}
		line("%s", row.String())
	}
	heading("06  邮件与 DNS 黑名单")
	mail := object(report["Mail"])
	if mail["Status"] == "smtp-not-tested" {
		line("SMTP      未检测（无特权环境省略邮件端口探测）")
	} else {
		line("SMTP      %s", value(mail["Status"]))
		for _, k := range unionKeys(mail) {
			if k != "DNSBlacklist" && k != "Status" && k != "Reason" {
				line("%s%s", pad(k, 22), value(mail[k]))
			}
		}
	}
	dns := object(mail["DNSBlacklist"])
	line("DNS 查询  总数 %s · 未列黑 %s · 来源标记 %s", value(dns["Total"]), value(dns["Clean"]), value(dns["Marked"]))
	line("          来源列黑 %s · 查询未知 %s", value(dns["Blacklisted"]), value(dns["Unknown"]))
	line("\n\x1b[2m据历史报告整理；完整数据见页面 JSON 报告。")
	line("未知不代表安全或可访问；结果不衡量带宽、延迟或丢包。\x1b[0m")
	return b.String(), nil
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func value(v any) string {
	var text string
	switch x := v.(type) {
	case nil:
		return "未知"
	case bool:
		if x {
			return "是"
		}
		return "否"
	case string:
		text = x
	case float64:
		text = fmt.Sprint(x)
	default:
		return "未知"
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	switch strings.ToLower(text) {
	case "", "null", "undefined", "n/a", "unknown":
		return "未知"
	}
	runes := []rune(text)
	if len(runes) > 100 {
		text = string(runes[:99]) + "…"
	}
	return text
}
func pad(v string, n int) string {
	v = value(v)
	runes := []rune(v)
	cells := 0
	var b strings.Builder
	for i, r := range runes {
		w := 1
		if k := width.LookupRune(r).Kind(); k == width.EastAsianWide || k == width.EastAsianFullwidth {
			w = 2
		}
		if cells+w > n-2 {
			if i < len(runes) {
				b.WriteRune('…')
				cells++
			}
			break
		}
		b.WriteRune(r)
		cells += w
	}
	b.WriteString(strings.Repeat(" ", max(1, n-cells)))
	return b.String()
}
func unionKeys(groups ...map[string]any) []string {
	set := map[string]bool{}
	for _, g := range groups {
		for key := range g {
			set[key] = true
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 32 {
		keys = keys[:32]
	}
	return keys
}
func mediaStatus(v any) string {
	s := value(v)
	switch s {
	case "Yes", "解锁":
		return "可访问"
	case "Block", "屏蔽":
		return "受限"
	case "Failed", "失败":
		return "检测失败"
	case "Pending", "待定":
		return "待确认"
	}
	return s
}
