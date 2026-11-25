package agent

import (
	"context"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestHostNetworkPendingOrderWithinSecondAndRemovedCarrier(t *testing.T) {
	l := newHourlyLedger(t)
	at := time.Now().UTC().Truncate(time.Second)
	boot := "11111111-1111-1111-1111-111111111111"
	first := networkSample(at, boot, 100)
	first.Interfaces = append(first.Interfaces, model.HostNetworkInterface{Name: "eth1", Index: 3, Receive: 30, Send: 40})
	// Both removal and reset occur in the same second. RFC3339 lexical ordering
	// would put the whole-second checkpoint after its fractional successors.
	for _, sample := range []model.HostNetworkSample{first, networkSample(at.Add(time.Nanosecond), boot, 110), networkSample(at.Add(100*time.Millisecond), boot, 10)} {
		if err := l.recordHostNetwork(context.Background(), sample); err != nil {
			t.Fatal(err)
		}
	}
	_, samples, err := l.pendingHostNetwork()
	if err != nil || len(samples) != 3 {
		t.Fatal(samples, err)
	}
	for i, want := range []int64{100, 110, 10} {
		if samples[i].Interfaces[0].Receive != want {
			t.Fatal("checkpoint order lost", samples)
		}
	}
	if len(samples[0].Interfaces) != 2 {
		t.Fatal("removed NIC checkpoint lost")
	}
}

func TestHostNetworkPendingBatchBoundAndDrain(t *testing.T) {
	l := newHourlyLedger(t)
	at := time.Now().UTC()
	for i := 0; i < 12; i++ {
		// Alternating counter resets retain checkpoints while disconnected.
		if err := l.recordHostNetwork(context.Background(), networkSample(at.Add(time.Duration(i)*time.Second), "11111111-1111-1111-1111-111111111111", int64(100-i))); err != nil {
			t.Fatal(err)
		}
	}
	batch, samples, err := l.pendingHostNetwork()
	if err != nil || len(samples) != 8 {
		t.Fatal("unbounded report", len(samples), err)
	}
	if err := l.acknowledgeHostNetwork(batch); err != nil {
		t.Fatal(err)
	}
	_, rest, err := l.pendingHostNetwork()
	if err != nil || len(rest) != 4 || !rest[0].At.After(samples[7].At) {
		t.Fatal("queue failed to drain in order", rest, err)
	}
}
