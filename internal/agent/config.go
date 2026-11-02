package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
)

// LoadConfig loads a private configuration file. Secrets are never included in errors.
func LoadConfig(path string) (model.AgentConfig, error) {
	var cfg model.AgentConfig
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return cfg, err
	}
	if info.Mode().Perm()&0077 != 0 {
		// Windows does not expose POSIX ownership bits; installed Linux files must be private.
		if err := privateConfigMode(info.Mode()); err != nil {
			return cfg, err
		}
	}
	d := json.NewDecoder(io.LimitReader(f, 64*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, errors.New("Agent 配置不是有效 JSON")
	}
	err = normalizeConfig(&cfg)
	return cfg, err
}

func normalizeConfig(cfg *model.AgentConfig) error {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:18081"
	}
	if cfg.StatePath == "" {
		cfg.StatePath = "/var/lib/hqip-agent/state.db"
	}
	if cfg.StatsURL == "" {
		cfg.StatsURL = "http://127.0.0.1:18082"
	}
	if cfg.HysteriaService == "" {
		cfg.HysteriaService = "hqip-hysteria.service"
	}
	if len(cfg.Token) < 24 || len(cfg.StatsSecret) < 24 {
		return errors.New("Agent token 与统计接口密钥至少需要 24 字符")
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil || !isLoopback(host) {
		return errors.New("Agent 鉴权接口必须监听 loopback IP")
	}
	u, err := url.Parse(cfg.StatsURL)
	if err != nil || u.Scheme != "http" || !isLoopback(u.Hostname()) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("统计接口必须使用本机 loopback HTTP 根地址")
	}
	u, err = url.Parse(cfg.ControlURL)
	if err != nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return errors.New("控制服务必须使用 HTTPS（本机开发可用 HTTP）")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("控制服务地址必须是站点根地址")
	}
	if strings.ContainsAny(cfg.HysteriaService, "\x00\r\n /\\") || strings.HasPrefix(cfg.HysteriaService, "-") {
		return fmt.Errorf("无效 Hysteria systemd 服务名")
	}
	cfg.ControlURL = strings.TrimRight(cfg.ControlURL, "/")
	cfg.StatsURL = strings.TrimRight(cfg.StatsURL, "/")
	return nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
