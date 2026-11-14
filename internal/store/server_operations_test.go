package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func managedServerFixture(t *testing.T) (*Store, *time.Time, model.Server, model.Node) {
	t.Helper()
	s, now := fixture(t)
	v, a, _ := serverPair(t, s)
	if _, err := s.DB.Exec("UPDATE jobs SET state='done'"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkInstalled(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveServerCredential(v.ID, "initial-password-123", v.Revision, 0); err != nil {
		t.Fatal(err)
	}
	return s, now, v, a
}
func TestManagedCredentialEncryptionHourlyChecksAndIdentity(t *testing.T) {
	s, now, v, _ := managedServerFixture(t)
	var encrypted string
	if err := s.DB.QueryRow("SELECT secret FROM server_credentials WHERE server_id=?", v.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, "initial-password") {
		t.Fatal("plaintext credential stored")
	}
	if err := s.CheckIntegrity(); err != nil {
		t.Fatal(err)
	}
	c, err := s.ServerCredential(v.ID)
	if err != nil || !c.Configured || c.Status != "healthy" || c.Revision != 1 {
		t.Fatalf("status: %+v %v", c, err)
	}
	data, _ := json.Marshal(c)
	if strings.Contains(string(data), encrypted) || strings.Contains(string(data), "initial-password") {
		t.Fatal("credential exposed")
	}
	if err = s.ScheduleCredentialChecks(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NextServerOperation(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("checked before due: %v", err)
	}
	*now = now.Add(time.Hour)
	for range 3 {
		if err = s.ScheduleCredentialChecks(); err != nil {
			t.Fatal(err)
		}
	}
	j, err := s.NextServerOperation()
	if err != nil || j.Kind != "check" {
		t.Fatalf("due check: %+v %v", j, err)
	}
	if err = s.DeleteServerCredential(v.ID, 1); err == nil {
		t.Fatal("deleted running credential")
	}
	if err = s.FinishServerOperation(j, "failed", "网络不可达", "unreachable"); err != nil {
		t.Fatal(err)
	}
	c, _ = s.ServerCredential(v.ID)
	if c.Status != "unreachable" || c.NextCheck.Unix() != now.Add(time.Hour).Unix() {
		t.Fatalf("failed check lost schedule: %+v", c)
	}
	if _, err = s.NextServerOperation(); !errors.Is(err, ErrNotFound) {
		t.Fatal("duplicate hourly check")
	}
	v.SSHPort = 30022
	v, err = s.UpdateServer(v, v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ServerPassword(v); err == nil {
		t.Fatal("credential sent to changed SSH target")
	}
}
func TestManagedPasswordRecoveryAndSerialization(t *testing.T) {
	s, _, v, _ := managedServerFixture(t)
	if _, err := s.QueueServerOperation(v.ID, "", "password", "forbidden-password", "", v.Revision); err == nil {
		t.Fatal("password mutation was queued")
	}
	// Rows written by the retired release must not execute on restart.
	for _, state := range []string{"pending", "running", "uncertain"} {
		id := NewID()
		if _, err := s.DB.Exec("INSERT INTO server_operations(id,server_id,kind,state,credential_revision,binding,secret,created_at) VALUES(?,?,'password',?,1,'old','legacy-secret',?)", id, v.ID, state, s.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.NextServerOperation(); !errors.Is(err, ErrNotFound) {
			t.Fatal("legacy password task was claimable", err)
		}
		if err := s.RecoverServerOperations(); err != nil {
			t.Fatal(err)
		}
		var savedState, secret string
		if err := s.DB.QueryRow("SELECT state,secret FROM server_operations WHERE id=?", id).Scan(&savedState, &secret); err != nil || savedState != "failed" || secret != "" {
			t.Fatal("legacy mutation not retired")
		}
		if err := s.RetryServerOperation(id); err == nil {
			t.Fatal("legacy task could be revived")
		}
	}
	if err := s.SaveServerCredential(v.ID, "provider-new-password", v.Revision, 1); err != nil {
		t.Fatal(err)
	}
	if password, err := s.ServerPassword(v); err != nil || password != "provider-new-password" {
		t.Fatal("credential update blocked by retired job")
	}
}
func TestManagedAgentUpgradeUsesHostQueueWithoutChangingNodeStatus(t *testing.T) {
	s, _, v, n := managedServerFixture(t)
	before, err := s.Node(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.QueueServerOperation(v.ID, n.ID, "agent-upgrade", "", "release", 0)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.NextServerOperation()
	if err != nil || op.ID != queued.ID {
		t.Fatal(err)
	}
	if _, err = s.Queue(n.ID, "verify", ""); err != nil {
		t.Fatal(err)
	}
	before, err = s.Node(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.NextJob(); !errors.Is(err, ErrNotFound) {
		t.Fatal("node job ignored server operation lock")
	}
	if err = s.RecoverServerOperations(); err != nil {
		t.Fatal(err)
	}
	op, err = s.NextServerOperation()
	if err != nil || op.ID != queued.ID {
		t.Fatal("restart lost job identity")
	}
	if err = s.FinishServerOperation(op, "succeeded", "已升级", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NextJob(); err != nil {
		t.Fatal(err)
	}
	after, err := s.Node(n.ID)
	if err != nil || before.Status != after.Status {
		t.Fatalf("Agent upgrade changed readiness: %v", err)
	}
}
