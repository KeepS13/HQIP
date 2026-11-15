package store

import (
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestAlertOperationLogLinkSurvivesEvaluationAndSystemFiltering(t *testing.T) {
	s, _ := fixture(t)
	alertEval(t, s, model.NotificationCondition{Key: "controller:backup", Category: "system", Kind: "backup", ResourceID: "controller", Title: "自动备份需要检查", Message: "最近备份失败", DetailPath: "/operation-logs", Level: "danger", Continuous: true})
	page, err := s.ListAlerts(1, 20, "system", "", "current", "", false)
	if err != nil || len(page.Items) != 1 || page.Items[0].Condition.DetailPath != "/operation-logs" {
		t.Fatal("backup alert link/category lost", page, err)
	}
	for _, path := range []string{"https://example.com/operation-logs", "//example.com/operation-logs", "/operation-logs?redirect=https://example.com", "/operation-logs/../users", "javascript:alert(1)"} {
		if got := alertPath(path); got != "/notifications" {
			t.Fatalf("unsafe alert path accepted: %q -> %q", path, got)
		}
	}
}
