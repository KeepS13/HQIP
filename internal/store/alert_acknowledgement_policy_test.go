package store

import (
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestDeploymentFailuresDoNotRequireAcknowledgement(t *testing.T) {
	s, _ := fixture(t)
	newNode(t, s, "failed-install-policy")
	job, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	s.FinishJob(job, false)

	// Other current kinds, including verification/cleanup jobs and a title that
	// mentions deployment, must retain the existing confirmation requirement.
	kinds := []string{"job.verify", "job.retire", "node.health", "certificate.issuance", "certificate.application", "server.month", "server.cycle", "server.expiry", "legacy", ""}
	conditions := make([]model.NotificationCondition, 0, len(kinds))
	for _, kind := range kinds {
		conditions = append(conditions, model.NotificationCondition{Key: "policy:" + kind, Kind: kind, Category: "system", Title: "部署失败相关提醒", Message: "Check condition", Level: "danger", Stage: 1})
	}
	alertEval(t, s, conditions...)
	items := alertList(t, s)
	if len(items) != len(kinds)+1 {
		t.Fatalf("got %d alerts, want %d", len(items), len(kinds)+1)
	}
	var deployment model.Alert
	for _, a := range items {
		if a.Condition.Kind == model.DeploymentFailureKind {
			deployment = a
			if a.AcknowledgementRequired || a.Acknowledged || a.AcknowledgedAt != nil || a.AcknowledgedBy != "" {
				t.Fatalf("deployment failure acquired a confirmation requirement or fabricated acknowledgement: %+v", a)
			}
		} else if !a.AcknowledgementRequired {
			t.Fatalf("unrelated alert no longer requires acknowledgement: %s", a.Condition.Kind)
		}
	}
	if deployment.ID == "" {
		t.Fatal("actual failed installation did not produce its alert")
	}
	summary, err := s.AlertSummary()
	if err != nil || summary.Active != len(items) || summary.Critical != len(items) || summary.Unacknowledged != len(kinds) {
		t.Fatalf("summary lost failures or counted them as unacknowledged: %+v, %v", summary, err)
	}
	unacknowledged, err := s.ListAlerts(1, 100, "", "", "current", "", true)
	if err != nil || unacknowledged.Total != len(kinds) || len(unacknowledged.Items) != len(kinds) {
		t.Fatalf("unacknowledged filter inconsistent with summary: %+v, %v", unacknowledged, err)
	}
	for _, a := range unacknowledged.Items {
		if !a.AcknowledgementRequired {
			t.Fatal("deployment failure survived unacknowledged filter")
		}
	}
	if _, err = s.UpdateAlert(deployment.ID, "admin", model.AlertUpdate{ExpectedRevision: deployment.Revision, Acknowledge: true, Note: "must not write"}); err == nil {
		t.Fatal("deployment failure accepted a manual acknowledgement")
	}
	detail, err := s.Alert(deployment.ID)
	if err != nil || detail.Revision != deployment.Revision || detail.Acknowledged || len(detail.Activities) != 1 {
		t.Fatalf("rejected acknowledgement mutated the alert: %+v, %v", detail, err)
	}
	detail, err = s.UpdateAlert(deployment.ID, "admin", model.AlertUpdate{ExpectedRevision: deployment.Revision, Note: "正在重新部署"})
	if err != nil || detail.Acknowledged || detail.AcknowledgementRequired || detail.Status != "active" || len(detail.Activities) != 2 {
		t.Fatalf("deployment failure no longer accepts independent notes: %+v, %v", detail, err)
	}
	ordinary := unacknowledged.Items[0]
	detail, err = s.UpdateAlert(ordinary.ID, "admin", model.AlertUpdate{ExpectedRevision: ordinary.Revision, Acknowledge: true})
	if err != nil || !detail.Acknowledged || !detail.AcknowledgementRequired {
		t.Fatalf("ordinary alert acknowledgement regressed: %+v, %v", detail, err)
	}
	summary, err = s.AlertSummary()
	if err != nil || summary.Unacknowledged != len(kinds)-1 {
		t.Fatalf("ordinary confirmation did not reduce pending count: %+v, %v", summary, err)
	}
}

func TestDeploymentAcknowledgementPolicyAppliesToExistingRecords(t *testing.T) {
	s, now := fixture(t)
	c := model.NotificationCondition{Key: "old-deployment", Kind: model.DeploymentFailureKind, Category: "system", Title: "旧部署失败", Message: "Existing failure", Level: "danger", Stage: 1}
	alertEval(t, s, c)
	a := alertList(t, s)[0]
	// Existing records contain only the condition and their real acknowledgement
	// history; the policy must work immediately without a rewrite or reevaluation.
	notificationExec(t, s, "UPDATE alerts SET acknowledged=1,ack_by='previous-admin',ack_at=? WHERE id=?", now.Unix(), a.ID)
	other, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.Now = s.Now
	detail, err := other.Alert(a.ID)
	if err != nil || detail.AcknowledgementRequired || !detail.Acknowledged || detail.AcknowledgedBy != "previous-admin" || detail.AcknowledgedAt == nil || detail.AcknowledgedAt.Unix() != now.Unix() {
		t.Fatalf("existing deployment policy/history not preserved: %+v, %v", detail, err)
	}
	notificationExec(t, other, "UPDATE alerts SET acknowledged=0,ack_by='',ack_at=0 WHERE id=?", a.ID)
	summary, err := other.AlertSummary()
	if err != nil || summary.Unacknowledged != 0 || summary.Active != 1 {
		t.Fatalf("existing failure still needs acknowledgement: %+v, %v", summary, err)
	}
	detail, err = other.Alert(a.ID)
	if err != nil || detail.AcknowledgementRequired || detail.Acknowledged || detail.AcknowledgedAt != nil || detail.AcknowledgedBy != "" {
		t.Fatalf("existing failure received a fabricated acknowledgement: %+v, %v", detail, err)
	}
}

func TestAlertReadMarkersDoNotDisplaceProcessingTimeline(t *testing.T) {
	s, now := fixture(t)
	alertEval(t, s, model.NotificationCondition{Key: "timeline", Kind: "node.health", Category: "system", Title: "节点异常", Stage: 1})
	a := alertList(t, s)[0]
	for i := 0; i < 201; i++ {
		notificationExec(t, s, "INSERT INTO alert_activity(alert_id,kind,message,actor,at) VALUES(?,'read','管理员已阅读','admin',?)", a.ID, now.Unix())
	}
	detail, err := s.Alert(a.ID)
	if err != nil || len(detail.Activities) != 1 || detail.Activities[0].Kind != "opened" {
		t.Fatalf("read markers displaced incident history: %+v, %v", detail.Activities, err)
	}
}
