package store

import (
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestHostNetworkRemovedCarrierRetainsTotalsAndRatesRecover(t *testing.T) {
	s, now := fixture(t)
	server, a, _ := serverPair(t, s)
	boot := "11111111-1111-1111-1111-111111111111"
	first := hostSample(*now, boot, 100, 200)
	first.Interfaces = append(first.Interfaces, model.HostNetworkInterface{Name: "eth1", Index: 3, Receive: 50, Send: 75})
	if err := sendNetwork(s, a, first); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		*now = now.Add(5 * time.Second)
		if err := sendNetwork(s, a, hostSample(*now, boot, int64(100+10*i), int64(200+20*i))); err != nil {
			t.Fatal(err)
		}
		u, err := s.HostNetwork(server.ID)
		if err != nil || !u.Discontinuity || u.Receive != int64(150+10*i) || u.Send != int64(275+20*i) {
			t.Fatal("removed carrier total lost", u, err)
		}
		if i == 1 && u.ReceiveRate != nil || i == 2 && (u.ReceiveRate == nil || *u.ReceiveRate != 2) {
			t.Fatal("rate failed to recover after topology settled", u)
		}
	}
}
