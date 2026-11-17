package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

func serverPair(t *testing.T, s *Store) (model.Server, model.Node, model.Node) {
	t.Helper()
	v, err := s.CreateServer(model.Server{Name: "multi-ip", SSHHost: "ssh.example.com", HostKey: "SHA256:pinned", MachineID: "machine-1"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAddress(v.ID, "203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateAddress(v.ID, "203.0.113.12")
	if err != nil {
		t.Fatal(err)
	}
	n1, err := s.CreateNode(model.Node{Name: "a", ServerID: v.ID, AddressID: a.ID, Domain: "a.example.com", Port: 443}, "password")
	if err != nil {
		t.Fatal(err)
	}
	n2, err := s.CreateNode(model.Node{Name: "b", ServerID: v.ID, AddressID: b.ID, Domain: "b.example.com", Port: 443}, "password")
	if err != nil {
		t.Fatal(err)
	}
	return v, n1, n2
}
func TestServerIPIsolationAndJobSerialization(t *testing.T) {
	s, _ := fixture(t)
	v, a, b := serverPair(t, s)
	if a.PublicIP == b.PublicIP || a.AuthPort == b.AuthPort || a.StatsPort == b.StatsPort || a.SSHHost != v.SSHHost || a.HostKey != v.HostKey {
		t.Fatalf("IP instance resolution failed: %+v %+v", a, b)
	}
	if _, err := s.CreateAddress(v.ID, a.PublicIP); err == nil {
		t.Fatal("duplicate IP allowed")
	}
	if err := s.DeleteAddress(v.ID, a.AddressID); err == nil {
		t.Fatal("in-use address deleted")
	}
	if _, err := s.CreateNode(model.Node{Name: "duplicate", ServerID: v.ID, AddressID: a.AddressID, Domain: "c.example.com", Port: 443}, "password"); err == nil {
		t.Fatal("duplicate IP listener allowed")
	}
	if _, err := s.CreateNode(model.Node{Name: "another-port", ServerID: v.ID, AddressID: a.AddressID, Domain: "d.example.com", Port: 8443}, "password"); err == nil {
		t.Fatal("same address split into another node by changing port")
	}
	if _, err := s.CreateServer(model.Server{Name: "alias", SSHHost: "alias.example.com", HostKey: v.HostKey, MachineID: v.MachineID}); err == nil {
		t.Fatal("same physical server registered through another hostname")
	}
	j, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.NextJob(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("same server ran two installs: %v", err)
	}
	s.FinishJob(j, true)
	if _, err = s.NextJob(); err != nil {
		t.Fatal(err)
	}
	if err = s.PinServer(v.ID, "SHA256:other", "machine-1"); err == nil {
		t.Fatal("host identity replacement allowed")
	}
	v.Name = "new-name"
	if _, err = s.UpdateServer(v, v.Revision+1); !errors.Is(err, ErrConflict) {
		t.Fatal("server revision guard missing")
	}
	if err = s.DeleteServer(v.ID, v.Revision); err == nil {
		t.Fatal("server deleted while nodes exist")
	}
	d := newDevice(t, s, "one-device")
	pa, _ := s.Credential(a.ID, d.ID)
	pb, _ := s.Credential(b.ID, d.ID)
	if pa == pb {
		t.Fatal("IP credentials shared")
	}
	p, err := s.ClientProfile(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Nodes {
		if n.Server == "" || n.Server != n.SNI || n.SNI == n.PublicIP || n.PublicIP == "" {
			t.Fatal("profile must dial the node domain while retaining separate physical IP metadata")
		}
	}
}

func TestDeviceDeletionRevokesAndRetainsAccounting(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "node")
	d := newDevice(t, s, "device")
	token := strings.TrimPrefix(d.SubscriptionURL, s.BaseURL+"/sub/")
	if err := s.SetClientAccount(d.ID, "delete-device", "a-strong-device-password"); err != nil {
		t.Fatal(err)
	}
	login, err := s.ClientLogin("delete-device", "a-strong-device-password", "install-a", "pc")
	if err != nil {
		t.Fatal(err)
	}
	report(t, s, n, d, *now, 100, 200)
	deleted, err := s.DeleteDevice(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Enabled || deleted.DeletedAt == nil || deleted.SubscriptionURL != "" || deleted.Lifecycle != "revoking" {
		t.Fatalf("bad tombstone: %+v", deleted)
	}
	if _, err = s.SubscriptionDevice(token); err == nil {
		t.Fatal("deleted subscription remains valid")
	}
	if _, _, err = s.ClientIdentity(login.AccessToken); err == nil {
		t.Fatal("deleted device session remains valid")
	}
	if _, err = s.ClientRefresh(login.RefreshToken); err == nil {
		t.Fatal("deleted refresh remains valid")
	}
	on := true
	if _, err = s.UpdateDevice(d.ID, nil, &on, false); err == nil {
		t.Fatal("deleted device reactivated")
	}
	if err = s.SetClientAccount(d.ID, "revived", "a-strong-device-password"); err == nil {
		t.Fatal("deleted account recreated")
	}
	desired, err := s.Desired(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	var removal string
	for _, v := range desired.Devices {
		if v.ID == d.ID {
			if v.Enabled {
				t.Fatal("deleted authorization enabled")
			}
			removal = v.RemovalID
		}
	}
	if removal == "" {
		t.Fatal("missing durable revocation")
	}
	report(t, s, n, d, *now, 130, 250)
	err = s.Report(n.ID, model.AgentReport{Revision: desired.Revision, Removals: []model.AgentRemovalStatus{{ID: removal, DeviceID: d.ID, Status: "revoked"}}})
	if err != nil {
		t.Fatal(err)
	}
	deleted, _ = s.Device(d.ID)
	if deleted.TotalUpload != 130 || deleted.TotalDownload != 250 {
		t.Fatal("deletion lost late accounting")
	}
	list, _ := s.Devices()
	if len(list) != 0 {
		t.Fatal("completed tombstone visible in normal list")
	}
	// A missing device record is not a probe. Only the exact node probe may auth.
	enc, _ := s.Vault.Encrypt(secure.Token())
	if _, err = s.DB.Exec("INSERT INTO credentials(node_id,device_id,password) VALUES(?,'orphan',?)", n.ID, enc); err != nil {
		t.Fatal(err)
	}
	desired, _ = s.Desired(n.ID)
	for _, v := range desired.Devices {
		if v.ID == "orphan" && v.Enabled {
			t.Fatal("orphan granted probe authorization")
		}
	}
}

func TestNodeDeletionRequiresExplicitDefaultAndRetiresOnlyOneIP(t *testing.T) {
	s, _ := fixture(t)
	v, a, b := serverPair(t, s)
	d := newDevice(t, s, "device")
	assignment, _ := s.Assignment(d.ID)
	assignment, _ = s.Assign(d.ID, []string{a.ID, b.ID}, a.ID, assignment.Revision)
	if err := s.MarkInstalled(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteNode(a.ID, model.NodeDeleteOptions{ExpectedRevision: a.Revision}); err == nil {
		t.Fatal("default replaced implicitly")
	}
	still, _ := s.Assignment(d.ID)
	if still.Revision != assignment.Revision {
		t.Fatal("failed deletion changed assignment")
	}
	options := model.NodeDeleteOptions{ExpectedRevision: a.Revision, Replacements: map[string]model.NodeReplacement{d.ID: {DefaultNodeID: b.ID, ExpectedRevision: assignment.Revision - 1}}}
	if _, err := s.DeleteNode(a.ID, options); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale assignment accepted: %v", err)
	}
	options.Replacements[d.ID] = model.NodeReplacement{DefaultNodeID: b.ID, ExpectedRevision: assignment.Revision}
	deleted, err := s.DeleteNode(a.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Lifecycle != "retiring" {
		t.Fatal("installed node hidden before stop")
	}
	assignment, _ = s.Assignment(d.ID)
	if len(assignment.NodeIDs) != 1 || assignment.DefaultNodeID != b.ID {
		t.Fatalf("assignment not switched: %+v", assignment)
	}
	if err = s.SetStatus(a.ID, "ready", "late install", false); err == nil {
		t.Fatal("late installer resurrected deleted node")
	}
	if _, err = s.Queue(a.ID, "verify", ""); err == nil {
		t.Fatal("retiring node queued verification")
	}
	if err = s.DeleteAddress(v.ID, a.AddressID); err == nil {
		t.Fatal("retiring address removed before stop")
	}
	desired, _ := s.Desired(a.ID)
	if desired.Retirement == nil || desired.Retirement.Finalize {
		t.Fatal("stop command missing")
	}
	for _, grant := range desired.Devices {
		if grant.Enabled {
			t.Fatal("retiring node still authenticates")
		}
	}
	if err = s.Report(a.ID, model.AgentReport{Revision: desired.Revision, Retirement: &model.RetirementStatus{ID: desired.Retirement.ID, Stopped: true, Final: true}}); err == nil {
		t.Fatal("final ack accepted without persisted stop ack")
	}
	if err = s.Report(a.ID, model.AgentReport{Revision: desired.Revision, Retirement: &model.RetirementStatus{ID: desired.Retirement.ID, Stopped: true}}); err != nil {
		t.Fatal(err)
	}
	desired, _ = s.Desired(a.ID)
	if !desired.Retirement.Finalize {
		t.Fatal("finalization not durable")
	}
	if err = s.Report(a.ID, model.AgentReport{Revision: desired.Revision, Retirement: &model.RetirementStatus{ID: desired.Retirement.ID, Stopped: true, Final: true}}); err != nil {
		t.Fatal(err)
	}
	deleted, _ = s.Node(a.ID)
	if deleted.Lifecycle != "deleted" {
		t.Fatal("final tombstone missing")
	}
	other, _ := s.Node(b.ID)
	if other.Lifecycle != "active" {
		t.Fatal("sibling IP affected")
	}
	if err = s.DeleteAddress(v.ID, a.AddressID); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteDuringInstallRetainsCleanupUntilWorkerSettles(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "node")
	j, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if d.Lifecycle != "retiring" {
		t.Fatal("running installer was assumed uninstalled")
	}
	active, _ := s.JobActive(j.ID)
	if active {
		t.Fatal("cancelled job stayed active")
	}
	if _, err = s.QueueRetirement(n.ID, "password"); err == nil {
		t.Fatal("retirement races cancelling installer")
	}
	if err = s.MarkInstalled(n.ID); err != nil {
		t.Fatal(err)
	}
	s.FinishJob(j, false)
	d, _ = s.Node(n.ID)
	if !d.Managed || d.Lifecycle != "retiring" {
		t.Fatal("partial install lost cleanup obligation")
	}
	if _, err = s.QueueRetirement(n.ID, "password"); err != nil {
		t.Fatal(err)
	}
	cleanup, err := s.NextJob()
	if err != nil || cleanup.Kind != "retire" {
		t.Fatalf("cleanup not queued: %+v %v", cleanup, err)
	}
	if err = s.CompleteNodeRetirement(n.ID); err != nil {
		t.Fatal(err)
	}
	s.FinishJob(cleanup, true)
}

func TestCloudflareBlankDomainsAndExpiredSSHCredentials(t *testing.T) {
	s, now := fixture(t)
	v, a, b := serverPair(t, s)
	for _, n := range []model.Node{a, b} {
		_, err := s.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []model.Node{a, b} {
		if _, err := s.CreateNode(model.Node{Name: "cf", ServerID: v.ID, AddressID: n.AddressID, DNSMode: "cloudflare", Port: 443}, "secret"); err != nil {
			t.Fatal(err)
		}
	}
	*now = now.Add(25 * time.Hour)
	if err := s.Maintenance(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM nodes WHERE password!=''").Scan(&count); err != nil || count != 0 {
		t.Fatal("expired SSH password retained")
	}
}

func TestDeletionSupersedesPendingRemovalAndUninstalledNeedsNoAck(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "unused")
	d := newDevice(t, s, "never-connected")
	a, _ := s.Assignment(d.ID)
	if _, err := s.Assign(d.ID, nil, "", a.Revision); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteDevice(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.PendingSync || deleted.Lifecycle != "deleted" {
		t.Fatalf("uninstalled node incorrectly waits for ack: %+v", deleted)
	}
	removals, _ := s.Removals(d.ID)
	for _, r := range removals {
		if r.Status != "revoked" {
			t.Fatalf("superseded removal still pending: %+v", r)
		}
	}
	// Deleting an uninstalled node also settles associated removals and sends
	// an empty client configuration without inventing a default replacement.
	d2 := newDevice(t, s, "assigned")
	a, _ = s.Assignment(d2.ID)
	_, err = s.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision, Replacements: map[string]model.NodeReplacement{d2.ID: {ExpectedRevision: a.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	device, _ := s.Device(d2.ID)
	if device.PendingSync {
		t.Fatal("uninstalled deleted node left pending auth")
	}
	a, _ = s.Assignment(d2.ID)
	if a.DefaultNodeID != "" || len(a.NodeIDs) > 0 {
		t.Fatal("last node delete did not produce empty config")
	}
}

func TestCertificateSwitchQueuedAtomicallyPreservesManualDNS(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "manual-domain")
	zone := strings.Repeat("a", 32)
	if _, err := s.QueueDeploymentCertificate(n.ID, "password", false, "", "cloudflare", zone); err == nil {
		t.Fatal("duplicate active job accepted")
	}
	unchanged, _ := s.Node(n.ID)
	if unchanged.CertificateMode != "http" || unchanged.ZoneID != "" {
		t.Fatal("failed queue partially changed certificate mode")
	}
	j, _ := s.NextJob()
	s.FinishJob(j, true)
	updated, err := s.QueueDeploymentCertificate(n.ID, "password", false, "", "cloudflare", zone)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CertificateMode != "http" {
		t.Fatal("queued target overwritten live certificate mode")
	}
	j, err = s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if j.CertificateMode != "cloudflare" || j.ZoneID != zone {
		t.Fatal("certificate target not persisted on job")
	}
	if err = s.CommitNodeCertificateMode(n.ID, j.ID, "cloudflare", zone); err != nil {
		t.Fatal(err)
	}
	updated, _ = s.Node(n.ID)
	if updated.DNSMode != "manual" || updated.Domain != n.Domain || updated.CertificateMode != "cloudflare" || updated.ZoneID != zone {
		t.Fatalf("certificate switch changed DNS ownership: %+v", updated)
	}
}

func TestInvalidCertificateReportDoesNotCommitNodeStatus(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "invalid-cert-report")
	desired, _ := s.Desired(n.ID)
	err := s.Report(n.ID, model.AgentReport{Revision: desired.Revision, Capabilities: []string{"spoof"}, Certificate: &model.CertificateStatus{Version: 999, NotAfter: time.Now()}})
	if err == nil {
		t.Fatal("unissued certificate accepted")
	}
	after, _ := s.Node(n.ID)
	if after.Certificate != nil || after.LastSeen != nil || len(after.Capabilities) > 0 {
		t.Fatal("rejected report partially poisoned node status")
	}
}

func TestPartialTakeoverPreservesLegacyInstallationVersion(t *testing.T) {
	s, _ := fixture(t)
	n := newNode(t, s, "legacy-rollback")
	if err := s.SetInstallationVersion(n.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkInstalled(n.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Node(n.ID)
	if !after.Managed || after.InstallationVersion != 1 {
		t.Fatal("failed takeover mislabeled legacy installation as v2")
	}
	if err := s.SetInstallationVersion(n.ID, 2); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Node(n.ID)
	if after.InstallationVersion != 2 {
		t.Fatal("successful installation version not recorded")
	}
	if _, err := s.DeleteNode(n.ID, model.NodeDeleteOptions{ExpectedRevision: n.Revision}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInstallationVersion(n.ID, 1); err == nil {
		t.Fatal("late worker changed retiring installation version")
	}
}
