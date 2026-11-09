package store

import (
	"os"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func fixture(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	return backendFixture(t, os.Getenv("HQIP_TEST_POSTGRES_URL") != "")
}

// Legacy SQLite schema migrations always exercise their source dialect, even
// when the business repository regression suite targets PostgreSQL.
func sqliteFixture(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	return backendFixture(t, false)
}

func backendFixture(t *testing.T, postgres bool) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(t.TempDir(), "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if postgres {
		s = postgresFixture(t, s)
	}
	now := time.Date(2026, 10, 31, 23, 59, 0, 0, Shanghai)
	s.Now = func() time.Time { return now }
	t.Cleanup(func() { s.Close() })
	return s, &now
}
func newNode(t *testing.T, s *Store, name string) model.Node {
	t.Helper()
	n, err := s.CreateNode(model.Node{Name: name, SSHHost: name + ".example.com", SSHPort: 22, SSHUser: "root", PublicIP: "203.0.113.8", Domain: name + ".example.com", Port: 443}, "private-ssh-password")
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func newDevice(t *testing.T, s *Store, name string) model.Device {
	t.Helper()
	d, err := s.CreateDevice(name)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := s.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	defaultID := ""
	if len(ids) > 0 {
		defaultID = ids[0]
	}
	a, err := s.Assignment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Assign(d.ID, ids, defaultID, a.Revision); err != nil {
		t.Fatal(err)
	}
	return d
}
func report(t *testing.T, s *Store, n model.Node, d model.Device, at time.Time, up, down int64) {
	t.Helper()
	desired, err := s.Desired(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Report(n.ID, model.AgentReport{Revision: desired.Revision, CoreHealthy: true, Online: map[string]int{d.ID: 1, ProbeID(n.ID): 1}, Daily: []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(at), Upload: up, Download: down}}, Minutes: []model.UsageBucket{{DeviceID: d.ID, Start: at.Unix() / 60 * 60, Upload: up, Download: down}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUsageIdempotenceMonthBoundaryRestartAndRetention(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "node-a")
	d := newDevice(t, s, "电脑")
	oct := *now
	report(t, s, n, d, oct, 100, 300)
	report(t, s, n, d, oct, 100, 300)
	report(t, s, n, d, oct, 90, 320)
	got, err := s.Device(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalUpload != 100 || got.TotalDownload != 320 {
		t.Fatalf("replay inflated totals: %+v", got)
	}
	*now = now.Add(2 * time.Minute)
	report(t, s, n, d, *now, 20, 40)
	got, _ = s.Device(d.ID)
	if got.MonthUpload != 20 || got.MonthDownload != 40 || got.TotalUpload != 120 || got.TotalDownload != 360 {
		t.Fatalf("Shanghai month boundary incorrect: %+v", got)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := reopenFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Now = func() time.Time { return *now }
	report(t, reopened, n, d, *now, 20, 40)
	got, _ = reopened.Device(d.ID)
	if got.TotalUpload != 120 || got.TotalDownload != 360 {
		t.Fatalf("restart replay inflated totals: %+v", got)
	}
	*now = now.AddDate(1, 2, 0)
	if err = reopened.Maintenance(); err != nil {
		t.Fatal(err)
	}
	report(t, reopened, n, d, oct, 999, 999)
	got, _ = reopened.Device(d.ID)
	if got.TotalUpload != 120 || got.TotalDownload != 360 || got.MonthUpload != 0 {
		t.Fatalf("retention changed lifetime or accepted expired replay: %+v", got)
	}
}

func TestRevocationRotationAndFreshDeviceDedup(t *testing.T) {
	s, now := fixture(t)
	a := newNode(t, s, "node-a")
	b := newNode(t, s, "node-b")
	d := newDevice(t, s, "电脑")
	other := newDevice(t, s, "手机")
	report(t, s, a, d, *now, 10, 20)
	report(t, s, b, d, *now, 30, 40)
	got, _ := s.Device(d.ID)
	if !got.Online || got.Instances != 2 || got.PendingSync {
		t.Fatalf("cross-node device aggregation: %+v", got)
	}
	node, _ := s.Node(a.ID)
	if node.OnlineDevices != 1 || node.Instances != 1 {
		t.Fatalf("internal probe was counted: %+v", node)
	}
	before, _ := s.Credential(a.ID, d.ID)
	beforeOther, _ := s.Credential(a.ID, other.ID)
	oldURL := got.SubscriptionURL
	enabled := false
	got, err := s.UpdateDevice(d.ID, nil, &enabled, false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.PendingSync || got.Enabled {
		t.Fatalf("revocation must await agent ack: %+v", got)
	}
	desired, _ := s.Desired(a.ID)
	for _, entry := range desired.Devices {
		if entry.ID == d.ID && entry.Enabled {
			t.Fatal("revoked device remains enabled in desired config")
		}
	}
	if _, err = s.UpdateDevice(d.ID, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Credential(a.ID, d.ID)
	afterOther, _ := s.Credential(a.ID, other.ID)
	got, _ = s.Device(d.ID)
	if before == after || beforeOther != afterOther || oldURL == got.SubscriptionURL {
		t.Fatal("rotation did not isolate and replace device credentials")
	}
	for _, n := range []model.Node{a, b} {
		desired, _ = s.Desired(n.ID)
		if err = s.Report(n.ID, model.AgentReport{Revision: desired.Revision, CoreHealthy: true, Online: map[string]int{}}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.Device(d.ID)
	if got.PendingSync || got.Online {
		t.Fatalf("agent ack did not settle revocation: %+v", got)
	}
	*now = now.Add(31 * time.Second)
	node, _ = s.Node(a.ID)
	if node.OnlineDevices != 0 || node.Metrics.UploadRate != 0 {
		t.Fatal("stale heartbeats must not claim online usage")
	}
}

func TestRecoveryTTLAndAccountingIsolation(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "node")
	d := newDevice(t, s, "电脑")
	job, err := s.NextJob()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverJobs(); err != nil {
		t.Fatal(err)
	}
	again, err := s.NextJob()
	if err != nil || again.ID != job.ID {
		t.Fatal("running job was not durably recovered")
	}
	desired, _ := s.Desired(n.ID)
	err = s.Report(n.ID, model.AgentReport{Revision: desired.Revision, Daily: []model.UsageBucket{{DeviceID: "unknown", Start: DayStart(*now), Upload: 100}, {DeviceID: ProbeID(n.ID), Start: DayStart(*now), Upload: 100}, {DeviceID: d.ID, Start: DayStart(*now), Upload: 5}}})
	if err != nil {
		t.Fatal(err)
	}
	node, _ := s.Node(n.ID)
	if node.TotalUpload != 5 {
		t.Fatalf("foreign/probe credentials polluted usage: %d", node.TotalUpload)
	}
	err = s.Report(n.ID, model.AgentReport{Revision: desired.Revision, Daily: []model.UsageBucket{{DeviceID: d.ID, Start: DayStart(*now), Upload: -1}}})
	if err == nil {
		t.Fatal("accepted negative counters")
	}
	*now = now.Add(25 * time.Hour)
	if err = s.Maintenance(); err != nil {
		t.Fatal(err)
	}
	password, agent, probe, err := s.NodeSecrets(n.ID)
	if err != nil || password != "" || agent == "" || probe == "" {
		t.Fatal("SSH TTL expired other operational credentials")
	}
}

func TestHourlyReplayUsesOnlyPositiveMinuteDifference(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "node")
	d := newDevice(t, s, "电脑")
	report(t, s, n, d, *now, 100, 200)
	report(t, s, n, d, *now, 100, 200)
	report(t, s, n, d, *now, 120, 180)
	points, err := s.Traffic(n.ID, d.ID, "7d")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Upload != 120 || points[0].Download != 200 {
		t.Fatalf("hour aggregation double counts minute replay: %+v", points)
	}
}

func TestAgentRollbackAndUnknownCoreAreConservative(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "node")
	d := newDevice(t, s, "电脑")
	report(t, s, n, d, *now, 1, 2)
	desired, _ := s.Desired(n.ID)
	if err := s.Report(n.ID, model.AgentReport{Revision: desired.Revision - 1, CoreHealthy: false, Online: map[string]int{d.ID: 1}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Device(d.ID)
	if !got.PendingSync || got.Online || got.Instances != 0 {
		t.Fatalf("stale cached config or failed stats was treated as current: %+v", got)
	}
}

func TestNodeDeviceCountersAreNodeScoped(t *testing.T) {
	s, now := fixture(t)
	a := newNode(t, s, "a")
	b := newNode(t, s, "b")
	d := newDevice(t, s, "电脑")
	report(t, s, a, d, *now, 1, 2)
	report(t, s, b, d, *now, 100, 200)
	devices, err := s.NodeDevices(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].MonthUpload != 1 || devices[0].TotalDownload != 2 || devices[0].Instances != 1 {
		t.Fatalf("node table included global device counters: %+v", devices)
	}
}

func TestStaleMetadataEditCannotErasePinOrDeploymentState(t *testing.T) {
	s, _ := fixture(t)
	stale := newNode(t, s, "node")
	if err := s.Pin(stale.ID, "SHA256:trusted"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(stale.ID, "awaiting_external", "installed", true); err != nil {
		t.Fatal(err)
	}
	stale.Name = "renamed"
	if err := s.SaveNode(stale); err != nil {
		t.Fatal(err)
	}
	n, err := s.Node(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.HostKey != "SHA256:trusted" || n.Status != "awaiting_external" || !n.SharedHost || n.Name != "renamed" {
		t.Fatalf("stale metadata overwrote worker fields: %+v", n)
	}
	if err = s.Pin(n.ID, "SHA256:attacker"); err == nil {
		t.Fatal("different fingerprint was accepted after stale edit")
	}
	if _, err = s.Queue(n.ID, "verify", ""); err == nil {
		t.Fatal("initial deployment job should still prevent second active task")
	}
}

func TestRestoreAdvancesEpochAndInvalidatesStaleSessions(t *testing.T) {
	s, now := fixture(t)
	n := newNode(t, s, "node")
	d := newDevice(t, s, "电脑")
	report(t, s, n, d, *now, 10, 20)
	if err := s.InitAdmin("admin", "restore-test-password"); err != nil {
		t.Fatal(err)
	}
	_, hash, _, err := s.Admin()
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.CreateSession(hash)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileRestore(); err != nil {
		t.Fatal(err)
	}
	n, err = s.Node(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.DesiredRevision < now.UnixMilli() || n.DesiredRevision > 1<<53 || n.AppliedRevision != 0 || n.LastSeen != nil || n.CoreHealthy {
		t.Fatalf("restore epoch did not safely supersede live agent config: %+v", n)
	}
	if _, err = s.Session(token); err == nil {
		t.Fatal("restored snapshot resumed an obsolete browser session")
	}
	if n.TotalUpload != 10 || n.TotalDownload != 20 {
		t.Fatal("restore reconciliation changed accounting")
	}
}
