package store

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

// Infrastructure expiry is advisory. This condition never changes admission,
// desired node configuration, subscriptions or the billing cycle itself.
func serverExpiryCondition(id, name string, billing model.ServerBilling, now time.Time) (model.NotificationCondition, bool) {
	if billing.ExpiresAt == nil || billing.Status == "unconfigured" {
		return model.NotificationCondition{}, false
	}
	remaining := billing.ExpiresAt.Sub(now)
	stage, hours := 0, 0
	for i, h := range []int{48, 24, 12} {
		if remaining <= time.Duration(h)*time.Hour {
			stage, hours = i+1, h
		}
	}
	if remaining <= 0 {
		stage, hours = 4, 0
	}
	if stage == 0 {
		return model.NotificationCondition{}, false
	}
	level := "warning"
	message := fmt.Sprintf("服务器将于北京时间 %s 到期（剩余不超过 %d 小时），请在额度与续期中确认续费。", notificationDate(billing.ExpiresAt.Unix()), hours)
	if stage == 4 {
		level = "danger"
		message = "服务器已于北京时间 " + notificationDate(billing.ExpiresAt.Unix()) + " 到期；周期流量继续累计，续费后须手动填写新到期时间。"
	}
	message += " 此提醒仅供管理员处理，HQIP 不会停止服务器或节点。"
	return model.NotificationCondition{
		Key:      "server.expiry:" + id + ":" + strconv.FormatInt(billing.ExpiresAt.Unix(), 10),
		Category: "system", Kind: "server.expiry", ResourceID: id,
		Title: notificationName(name, "服务器") + " 到期提醒", Message: message,
		DetailPath: "/servers/" + url.PathEscape(id), Level: level, Stage: stage,
	}, true
}

func notificationBillingQuota(kind, id, name, path string, billing model.ServerBilling, quota *int64) (model.NotificationCondition, bool) {
	if quota == nil || *quota <= 0 {
		return model.NotificationCondition{}, false
	}
	used := billing.CycleUpload + billing.CycleDownload
	c, ok := notificationQuota(kind+".cycle", id, name, path, billing.CycleID, used, quota, []int{80, 100})
	if !ok {
		if billing.CycleComplete {
			return c, false
		}
		// A lower known subtotal cannot prove that an existing quota warning ended.
		return model.NotificationCondition{Key: kind + ".cycle:" + id + ":" + billing.CycleID,
			Category: "capacity", Kind: kind + ".cycle", ResourceID: id, Unknown: true}, true
	}
	c.Title = name + " 周期流量提醒"
	c.Message = fmt.Sprintf("本周期代理流量已使用 %s / %s，达到 %d%%；仅提醒，不限速、不断网。", notificationBytes(used), notificationBytes(*quota), c.Stage)
	if !billing.CycleComplete {
		c.Message += " 当前为已知用量合计，部分时段的数据不完整。"
	}
	return c, true
}
