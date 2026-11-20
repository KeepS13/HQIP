package store

import (
	"errors"
	"testing"

	"github.com/KeepS13/HQIP/internal/cloudflare"
	"github.com/KeepS13/HQIP/internal/model"
)

func ipSwitchFixture(t *testing.T) (*Store, model.Server, model.Node) {
	t.Helper()
	s, _ := fixture(t)
	v, err := s.CreateServer(model.Server{Name: "switch", SSHHost: "8.8.8.8", HostKey: "pinned", MachineID: "machine"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateNode(model.Node{ServerID: v.ID, PublicIP: v.SSHHost, Name: "original", Domain: "switch.example.com", Port: 443}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkInstalled(n.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishJob(j, true); err != nil {
		t.Fatal(err)
	}
	v.SSHHost = "1.1.1.1"
	v, err = s.UpdateServer(v, v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	n, err = s.Node(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, v, n
}

func TestNodeIPSwitchPreservesIdentityAndRecovers(t *testing.T) {
	s, v, n := ipSwitchFixture(t)
	_, token, probe, err := s.NodeSecrets(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO lifetime(node_id,device_id,upload,download) VALUES(?,?,123,456)", n.ID, "device"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		from, to           string
		nodeRev, serverRev int64
		dns                bool
	}{
		{n.PublicIP, v.SSHHost, n.Revision - 1, v.Revision, true},
		{n.PublicIP, v.SSHHost, n.Revision, v.Revision - 1, true},
		{"9.9.9.9", v.SSHHost, n.Revision, v.Revision, true},
		{n.PublicIP, "9.9.9.9", n.Revision, v.Revision, true},
		{n.PublicIP, "10.0.0.1", n.Revision, v.Revision, true},
		{n.PublicIP, v.SSHHost, n.Revision, v.Revision, false},
	} {
		if _, err = s.QueueNodeIPSwitch(n.ID, bad.from, bad.to, "secret", bad.nodeRev, bad.serverRev, bad.dns); err == nil {
			t.Fatal("invalid switch accepted", bad)
		}
	}
	addresses, _ := s.Addresses(v.ID)
	if len(addresses) != 1 {
		t.Fatal("rejected switch leaked address")
	}
	next, err := s.QueueNodeIPSwitch(n.ID, n.PublicIP, v.SSHHost, "secret", n.Revision, v.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != n.ID || next.Domain != n.Domain || next.PublicIP != v.SSHHost || next.AuthPort != n.AuthPort || next.StatsPort != n.StatsPort || next.IPSwitch == nil || next.IPSwitch.FromIP != n.PublicIP || next.Status != "pending" {
		t.Fatalf("bad switch: %+v", next)
	}
	_, tokenAfter, probeAfter, err := s.NodeSecrets(n.ID)
	if err != nil || token != tokenAfter || probe != probeAfter {
		t.Fatal("switch rotated credentials", err)
	}
	var upload, download int64
	if err = s.DB.QueryRow("SELECT upload,download FROM lifetime WHERE node_id=? AND device_id='device'", n.ID).Scan(&upload, &download); err != nil || upload != 123 || download != 456 {
		t.Fatal("usage lost", err)
	}
	if err = s.DeleteAddress(v.ID, n.AddressID); err == nil {
		t.Fatal("old address not reserved")
	}
	if _, err = s.CreateNode(model.Node{ServerID: v.ID, PublicIP: n.PublicIP, Name: "collision", Domain: "collision.example.com", Port: 443}, "secret"); !errors.Is(err, ErrAddressInUse) {
		t.Fatal("old IP can be reused during switch", err)
	}
	if _, err = s.Queue(n.ID, "verify", ""); err == nil {
		t.Fatal("verification bypasses pending deployment")
	}
	if err = s.CompleteNodeIPSwitch(n.ID, next.QueuedJobID, next.PublicIP); err == nil {
		t.Fatal("pending job completed before running")
	}
	j, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverJobs(); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.NextJob()
	if err != nil || recovered.ID != j.ID {
		t.Fatal("restart lost switch", err)
	}
	if err = s.FinishJob(recovered, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueDeployment(n.ID, "secret", false, ""); err != nil {
		t.Fatal("switch cannot retry", err)
	}
	j, err = s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteNodeIPSwitch(n.ID, j.ID, next.PublicIP); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishJob(j, true); err != nil {
		t.Fatal(err)
	}
	done, _ := s.Node(n.ID)
	if done.IPSwitch != nil || done.PublicIP != v.SSHHost {
		t.Fatal("switch not completed")
	}
	if err = s.DeleteAddress(v.ID, n.AddressID); err != nil {
		t.Fatal("old address not released", err)
	}
}

func TestNodeIPSwitchOccupiedTargetAndPendingJob(t *testing.T) {
	s, v, n := ipSwitchFixture(t)
	other, err := s.CreateNode(model.Node{ServerID: v.ID, PublicIP: v.SSHHost, Name: "other", Domain: "other.example.com", Port: 443}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueNodeIPSwitch(n.ID, n.PublicIP, v.SSHHost, "secret", n.Revision, v.Revision, true); !errors.Is(err, ErrAddressInUse) {
		t.Fatal("occupied target accepted", err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET lifecycle='retiring' WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueNodeIPSwitch(n.ID, n.PublicIP, v.SSHHost, "secret", n.Revision, v.Revision, true); !errors.Is(err, ErrAddressInUse) {
		t.Fatal("retiring target accepted", err)
	}
	if _, err = s.DB.Exec("UPDATE nodes SET lifecycle='deleted' WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueDeployment(n.ID, "secret", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueNodeIPSwitch(n.ID, n.PublicIP, v.SSHHost, "secret", n.Revision, v.Revision, true); err == nil {
		t.Fatal("pending installation overwritten")
	}
	current, _ := s.Node(n.ID)
	if current.PublicIP != n.PublicIP || current.IPSwitch != nil {
		t.Fatal("rejection modified original node")
	}
}

func TestNodeIPSwitchDNSPersistsOnlyExpectedRecord(t *testing.T) {
	s, v, n := ipSwitchFixture(t)
	if _, err := s.DB.Exec("UPDATE nodes SET config=json_set(config,'$.dnsMode','cloudflare') WHERE id=?", n.ID); err != nil {
		t.Fatal(err)
	}
	before := cloudflare.Record{Type: "A", Name: n.Domain, Content: n.PublicIP, TTL: 120, Comment: "HQIP node owned"}
	intent, err := s.CreateDNSIntent(n.ID, "11111111111111111111111111111111", "example.com", before)
	if err != nil {
		t.Fatal(err)
	}
	before.ID = "22222222222222222222222222222222"
	if err = s.CompleteDNSIntent(intent.ID, before); err != nil {
		t.Fatal(err)
	}
	intent, err = s.NodeDNSIntent(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueNodeIPSwitch(n.ID, n.PublicIP, v.SSHHost, "secret", n.Revision, v.Revision, false); err != nil {
		t.Fatal(err)
	}
	updated := before
	updated.Content = v.SSHHost
	wrong := updated
	wrong.Comment = "foreign"
	if err = s.CompleteIPSwitchDNS(intent, wrong); err == nil {
		t.Fatal("foreign DNS accepted")
	}
	if err = s.CompleteIPSwitchDNS(intent, updated); err != nil {
		t.Fatal(err)
	}
	stored, err := s.NodeDNSIntent(n.ID)
	if err != nil || stored.Record.ID != before.ID || stored.Record.Name != n.Domain || stored.Record.Content != v.SSHHost {
		t.Fatal("DNS identity not preserved", err)
	}
}
