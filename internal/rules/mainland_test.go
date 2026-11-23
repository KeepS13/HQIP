package rules

import (
	"net/netip"
	"strings"
	"testing"
)

// Match the exported scalar rules independently of the generator. Unknown
// domains use an overseas address to expose reliance on IP-only bypass rules.
func clashDecision(t *testing.T, rows []string, host, address string) string {
	t.Helper()
	ip := netip.MustParseAddr(address)
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, row := range rows {
		p := strings.Split(row, ",")
		matched := false
		switch p[0] {
		case "DOMAIN":
			matched = host == p[1]
		case "DOMAIN-SUFFIX":
			matched = host == p[1] || strings.HasSuffix(host, "."+p[1])
		case "DOMAIN-KEYWORD":
			matched = strings.Contains(host, p[1])
		case "IP-CIDR", "IP-CIDR6":
			matched = netip.MustParsePrefix(p[1]).Contains(ip)
		case "MATCH":
			return p[1]
		case "GEOIP":
			t.Fatal("export still depends on a client's GeoIP database")
		}
		if matched {
			return p[2]
		}
	}
	t.Fatal("missing terminal rule")
	return ""
}

func TestDefaultMainlandRouting(t *testing.T) {
	rows, err := Default().ClashRules("PROXY", "DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	domains := []string{
		"www.bilibili.com", "api.bilibili.com", "api.biliapi.net", "upos-sz-mirror.bilivideo.com",
		"i0.hdslb.com", "v.acgvideo.com", "www.douyin.com", "v.douyinvod.com", "www.kuaishou.com",
		"www.qq.com", "wx.qq.com", "v.qq.com", "music.163.com", "www.iqiyi.com", "www.youku.com",
		"www.taobao.com", "www.jd.com", "www.pinduoduo.com", "www.alipay.com", "www.meituan.com",
		"www.baidu.com", "www.zhihu.com", "www.xiaohongshu.com", "www.weibo.com", "www.12306.cn",
		"download.example.cn", "www.icbc.com.cn", "www.gov.cn",
	}
	for _, host := range domains {
		t.Run(host, func(t *testing.T) {
			if got := clashDecision(t, rows, host, "203.0.113.8"); got != "DIRECT" {
				t.Fatalf("%s routes to %s even with an overseas CDN address", host, got)
			}
		})
	}
	for _, host := range []string{"www.google.com", "www.youtube.com", "api.openai.com", "bilibili.com.example.org", "evilbilibili.com"} {
		if got := clashDecision(t, rows, host, "203.0.113.8"); got != "PROXY" {
			t.Errorf("overseas domain %s routes to %s", host, got)
		}
	}
	for _, ip := range []string{"223.5.5.5", "119.29.29.29", "2400:3200::1", "192.168.1.1", "fd00::1"} {
		if got := clashDecision(t, rows, "unlisted.example", ip); got != "DIRECT" {
			t.Errorf("mainland/private IP %s routes to %s", ip, got)
		}
	}
}
