package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"time"

	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/redis/go-redis/v9"
)

// AdminDecision describes only public security metadata, never credentials.
// RetryAfter is a conservative upper bound; inspecting the exact TTL would
// require widening the production Redis ACL.
type AdminDecision struct {
	Allowed    bool
	Reason     string
	RetryAfter int
	Failures   int
	BanLevel   int
	BanStarted bool
}

type AdminProtection interface {
	AdminAdmit(context.Context, string) (AdminDecision, error)
	AdminFailure(context.Context, string) (AdminDecision, error)
	AdminSuccess(context.Context, string) error
}

// AdminSource folds mapped IPv4 and IPv6 textual aliases, and groups IPv6
// privacy addresses by /64. Callers must first resolve the trusted proxy chain.
func AdminSource(address string) (string, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || ip.Zone() != "" {
		return "", errors.New("管理员登录来源无效")
	}
	ip = ip.Unmap()
	if ip.Is4() {
		return ip.String(), nil
	}
	return netip.PrefixFrom(ip, 64).Masked().String(), nil
}

// The script uses ONLY commands in the original restricted production ACL.
// Keys: global requests, source requests, 24h failures, 1m/15m/1h bans.
// Bans are one-member sorted sets whose score is the expiry timestamp. Denied
// requests never add failures, extend a ban, or consume the global budget.
var adminScript = redis.NewScript(`
local t = redis.call('TIME')
local now = t[1]*1000 + math.floor(t[2]/1000)
local mode = ARGV[1]
local durations = {60, 900, 3600}
local active = 0
for level = 1, 3 do
  redis.call('ZREMRANGEBYSCORE', KEYS[level+3], '-inf', now)
  if redis.call('ZCARD', KEYS[level+3]) > 0 then active = level end
end
if mode == 'success' then
  for i = 3, 6 do redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', '+inf') end
  return {1, 0, 0, 0, 0, 0}
end
if mode == 'failure' then
  redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now-86400000)
  redis.call('ZADD', KEYS[3], now, ARGV[2])
  redis.call('PEXPIRE', KEYS[3], 86400000)
  local failures = redis.call('ZCARD', KEYS[3])
  local level = 0
  if failures >= 20 then level = 3
  elseif failures >= 10 then level = 2
  elseif failures >= 5 then level = 1 end
  local started = 0
  if level > 0 and (active == 0 or level > active) then
    for i = 4, 6 do redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', '+inf') end
    redis.call('ZADD', KEYS[level+3], now+durations[level]*1000, 'ban')
    redis.call('PEXPIRE', KEYS[level+3], durations[level]*1000)
    started = 1
    active = level
  end
  if active > 0 then return {0, 1, durations[active], failures, active, started} end
  return {1, 0, 0, failures, 0, 0}
end
if active > 0 then return {0, 1, durations[active], 0, active, 0} end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now-900000)
if redis.call('ZCARD', KEYS[2]) >= 10 then return {0, 2, 900, 0, 0, 0} end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now-60000)
if redis.call('ZCARD', KEYS[1]) >= 30 then return {0, 3, 60, 0, 0, 0} end
redis.call('ZADD', KEYS[1], now, ARGV[2])
redis.call('PEXPIRE', KEYS[1], 60000)
redis.call('ZADD', KEYS[2], now, ARGV[2])
redis.call('PEXPIRE', KEYS[2], 900000)
return {1, 0, 0, 0, 0, 0}
`)

func (r *Redis) adminKeys(address string) ([]string, error) {
	source, err := AdminSource(address)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(source))
	base := r.prefix + "admin:" + hex.EncodeToString(h[:])
	return []string{r.prefix + "admin:global", base + ":requests", base + ":failures", base + ":ban:1", base + ":ban:2", base + ":ban:3"}, nil
}

func (r *Redis) admin(ctx context.Context, address, mode string) (AdminDecision, error) {
	keys, err := r.adminKeys(address)
	if err != nil {
		return AdminDecision{}, err
	}
	values, err := adminScript.Run(ctx, r.client, keys, mode, secure.Token()).Int64Slice()
	if err != nil || len(values) != 6 {
		return AdminDecision{}, errors.New("管理员登录防护暂不可用")
	}
	reasons := []string{"", "temporary_ban", "source_rate", "global_rate"}
	if values[1] < 0 || values[1] >= int64(len(reasons)) {
		return AdminDecision{}, errors.New("管理员登录防护暂不可用")
	}
	return AdminDecision{Allowed: values[0] == 1, Reason: reasons[values[1]], RetryAfter: int(values[2]), Failures: int(values[3]), BanLevel: int(values[4]), BanStarted: values[5] == 1}, nil
}

func (r *Redis) AdminAdmit(ctx context.Context, address string) (AdminDecision, error) {
	return r.admin(ctx, address, "admit")
}

func (r *Redis) AdminFailure(ctx context.Context, address string) (AdminDecision, error) {
	return r.admin(ctx, address, "failure")
}

func (r *Redis) AdminSuccess(ctx context.Context, address string) error {
	_, err := r.admin(ctx, address, "success")
	return err
}

func adminBan(failures int) (int, time.Duration) {
	switch {
	case failures >= 20:
		return 3, time.Hour
	case failures >= 10:
		return 2, 15 * time.Minute
	case failures >= 5:
		return 1, time.Minute
	default:
		return 0, 0
	}
}
