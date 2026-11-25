package store

import (
	"sync"
	"testing"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

func hostSample(at time.Time, boot string, rx, tx int64) model.HostNetworkSample {
	return model.HostNetworkSample{BootID: boot, BootAt: at.Add(-time.Hour), At: at, Interfaces: []model.HostNetworkInterface{{Name: "eth0", Index: 2, Receive: rx, Send: tx}}}
}

func sendNetwork(s *Store, node model.Node, samples ...model.HostNetworkSample) error {
	return s.Report(node.ID, model.AgentReport{Revision: 1, HostNetwork: samples})
}

func TestHostNetworkSharedHostReplayReorderAndProxyIsolation(t *testing.T) {
	s, now := fixture(t)
	server, a, b := serverPair(t, s)
	boot := "11111111-1111-1111-1111-111111111111"
	first := hostSample(*now, boot, 100, 200)
	for _, node := range []model.Node{a, b, a} {
		if err := sendNetwork(s, node, first); err != nil {
			t.Fatal(err)
		}
	}
	*now = now.Add(5 * time.Second)
	next := hostSample(*now, boot, 150, 275)
	if err := sendNetwork(s, b, next); err != nil {
		t.Fatal(err)
	}
	if err := sendNetwork(s, a, first); err != nil {
		t.Fatal(err)
	}
	u, err := s.HostNetwork(server.ID)
	if err != nil || u.Receive != 150 || u.Send != 275 || u.ReceiveRate == nil || *u.ReceiveRate != 10 || !u.Fresh {
		t.Fatalf("incorrect totals: %+v %v", u, err)
	}
	nodes, err := s.NodesWithAudience()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.HostNetwork == nil || node.HostNetwork.Receive != 150 {
			t.Fatal("missing shared host total", node)
		}
	}
	proxy, err := s.UsageTotals()
	if err != nil || proxy.TotalUpload != 0 || proxy.TotalDownload != 0 {
		t.Fatal("host counters leaked into proxy usage", proxy, err)
	}
	*now = now.Add(time.Minute)
	u, err = s.HostNetwork(server.ID)
	if err != nil || u.Fresh || u.ReceiveRate != nil || u.Receive != 150 {
		t.Fatal("stale counters cleared or rate stayed live", u, err)
	}
}

func TestHostNetworkResetRebootAndLatePreviousBoot(t *testing.T) {
	s, now := fixture(t)
	server, a, _ := serverPair(t, s)
	one, two := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	first := hostSample(*now, one, 100, 200)
	if err := sendNetwork(s, a, first); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(5 * time.Second)
	reset := hostSample(*now, one, 10, 20)
	if err := sendNetwork(s, a, reset); err != nil {
		t.Fatal(err)
	}
	late := hostSample(now.Add(time.Second), one, 15, 25)
	*now = now.Add(10 * time.Second)
	if err := sendNetwork(s, a, hostSample(*now, two, 30, 40), late); err != nil {
		t.Fatal(err)
	}
	u, err := s.HostNetwork(server.ID)
	if err != nil || u.Receive != 145 || u.Send != 265 || !u.Discontinuity || u.Sample.BootID != two || u.ReceiveRate != nil {
		t.Fatalf("reset/late checkpoint lost: %+v %v", u, err)
	}
	if err := sendNetwork(s, a, reset, first, late); err != nil {
		t.Fatal(err)
	}
	v, _ := s.HostNetwork(server.ID)
	if v.Receive != u.Receive || v.Send != u.Send {
		t.Fatal("replay changed totals")
	}
}

func TestHostNetworkConcurrentAgentsAndAtomicValidation(t *testing.T) {
	s, now := fixture(t)
	server, a, b := serverPair(t, s)
	boot := "11111111-1111-1111-1111-111111111111"
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			node := a
			if i%2 == 1 {
				node = b
			}
			if err := sendNetwork(s, node, hostSample(now.Add(time.Duration(i)*time.Second), boot, int64(100+i), int64(200+i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	u, err := s.HostNetwork(server.ID)
	if err != nil || u.Receive != 119 || u.Send != 219 {
		t.Fatal("same-host reports counted twice", u, err)
	}
	bad := hostSample(*now, boot, -1, 5)
	if err = sendNetwork(s, a, hostSample(now.Add(time.Minute), boot, 1000, 2000), bad); err == nil {
		t.Fatal("invalid batch accepted")
	}
	v, _ := s.HostNetwork(server.ID)
	if v.Receive != 119 || v.Send != 219 {
		t.Fatal("invalid batch partially applied")
	}
}
