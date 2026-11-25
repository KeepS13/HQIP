package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func networkSample(at time.Time, boot string, rx int64) model.HostNetworkSample {
	return model.HostNetworkSample{BootID: boot, BootAt: at.Add(-time.Hour), At: at, Interfaces: []model.HostNetworkInterface{{Name: "eth0", Index: 2, Receive: rx, Send: rx * 2}}}
}

func TestHostNetworkQueueSurvivesRestartAndAcknowledgmentRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	l, err := openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	first := networkSample(at, "11111111-1111-1111-1111-111111111111", 100)
	if err = l.recordHostNetwork(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	batch, _, err := l.pendingHostNetwork()
	if err != nil {
		t.Fatal(err)
	}
	next := first
	next.At = at.Add(5 * time.Second)
	next.Interfaces = []model.HostNetworkInterface{{Name: "eth0", Index: 2, Receive: 150, Send: 300}}
	if err = l.recordHostNetwork(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if err = l.acknowledgeHostNetwork(batch); err != nil {
		t.Fatal(err)
	}
	if err = l.close(); err != nil {
		t.Fatal(err)
	}
	l, err = openLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	batch, samples, err := l.pendingHostNetwork()
	if err != nil || len(samples) != 1 || samples[0].Interfaces[0].Receive != 150 {
		t.Fatalf("lost concurrent durable sample: %+v %v", samples, err)
	}
	if err = l.acknowledgeHostNetwork(batch); err != nil {
		t.Fatal(err)
	}
	_, samples, err = l.pendingHostNetwork()
	if err != nil || len(samples) != 0 {
		t.Fatal("ack failed", samples, err)
	}
}

func TestHostNetworkOfflineResetAndRebootPreserveOldCounters(t *testing.T) {
	l := newHourlyLedger(t)
	at := time.Now().UTC()
	for i, entry := range []struct {
		boot string
		rx   int64
	}{
		{"11111111-1111-1111-1111-111111111111", 100},
		{"11111111-1111-1111-1111-111111111111", 10},
		{"22222222-2222-2222-2222-222222222222", 20},
	} {
		if err := l.recordHostNetwork(context.Background(), networkSample(at.Add(time.Duration(i)*5*time.Second), entry.boot, entry.rx)); err != nil {
			t.Fatal(err)
		}
	}
	_, samples, err := l.pendingHostNetwork()
	if err != nil || len(samples) != 3 {
		t.Fatal(samples, err)
	}
	for i, want := range []int64{100, 10, 20} {
		if samples[i].Interfaces[0].Receive != want {
			t.Fatal("checkpoint order lost", samples)
		}
	}
}

func TestHostNetworkCapabilityChangesAtSameRevision(t *testing.T) {
	a := testAgent(t, "", "")
	d := desired(1, firstPassword, true)
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	d.HostNetworkVersion = 1
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	if a.cache.HostNetworkVersion != 1 {
		t.Fatal("capability ignored at unchanged configuration revision")
	}
	d.HostNetworkVersion = 0
	if err := a.applyDesired(d); err != nil {
		t.Fatal(err)
	}
	if a.cache.HostNetworkVersion != 0 {
		t.Fatal("rollback did not disable extension")
	}
}
