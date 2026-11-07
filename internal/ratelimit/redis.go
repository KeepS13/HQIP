// Package ratelimit contains shared admission controls, never account balances.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/redis/go-redis/v9"
)

// Redis owns one bounded connection pool. Keys contain hashes, not IPs/emails.
type Redis struct {
	client *redis.Client
	prefix string
}

// Use Redis time so all application instances enforce the same sliding window.
// A denied request does not extend the window or create an unbounded sorted set.
var windowScript = redis.NewScript(`
local t = redis.call('TIME')
local now = t[1]*1000 + math.floor(t[2]/1000)
local window = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now-window)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[2]) then return 0 end
redis.call('ZADD', KEYS[1], now, ARGV[3])
redis.call('PEXPIRE', KEYS[1], window)
return 1
`)

// Open validates transport and bounds connection costs. A remote Redis must use
// TLS with normal certificate verification; secrets are never returned in errors.
func Open(ctx context.Context, address, namespace string) (*Redis, error) {
	opts, err := redis.ParseURL(address)
	if err != nil {
		return nil, errors.New("Redis URL 无效")
	}
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return nil, errors.New("Redis 地址无效")
	}
	if opts.TLSConfig == nil && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		return nil, errors.New("远程 Redis 必须使用 rediss TLS 地址；本机可使用回环 IP")
	}
	if opts.TLSConfig != nil {
		opts.TLSConfig.MinVersion = tls.VersionTLS12
		opts.TLSConfig.InsecureSkipVerify = false
	}
	if namespace == "" || len(namespace) > 80 || strings.ContainsAny(namespace, "{} \r\n") {
		return nil, errors.New("Redis 命名空间无效")
	}
	opts.MaxRetries = -1
	opts.DialTimeout = time.Second
	opts.ReadTimeout = time.Second
	opts.WriteTimeout = time.Second
	opts.PoolTimeout = 250 * time.Millisecond
	opts.PoolSize = 16
	opts.MaxActiveConns = 16
	opts.ContextTimeoutEnabled = true
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, errors.New("Redis 连接或认证失败")
	}
	return &Redis{client: client, prefix: namespace + ":rate:"}, nil
}

func (r *Redis) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit < 1 || limit > 10000 || window < time.Millisecond || window > 24*time.Hour || key == "" {
		return false, errors.New("无效限流参数")
	}
	h := sha256.Sum256([]byte(key))
	n, err := windowScript.Run(ctx, r.client, []string{r.prefix + hex.EncodeToString(h[:])}, window.Milliseconds(), limit, secure.Token()).Int()
	if err != nil {
		return false, errors.New("共享限流服务暂不可用")
	}
	return n == 1, nil
}

func (r *Redis) Close() error { return r.client.Close() }

func (r *Redis) Ping(ctx context.Context) error {
	if r.client.Ping(ctx).Err() != nil {
		return errors.New("共享限流服务暂不可用")
	}
	return nil
}
