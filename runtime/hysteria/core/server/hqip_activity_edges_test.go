package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type activityPacket struct{ closed atomic.Bool }

func (p *activityPacket) ReadFrom([]byte) (int, string, error)    { return 0, "", net.ErrClosed }
func (p *activityPacket) WriteTo(b []byte, _ string) (int, error) { return len(b), nil }
func (p *activityPacket) SetReadDeadline(time.Time) error         { return nil }
func (p *activityPacket) Close() error                            { p.closed.Store(true); return nil }

type mixedActivityOutbound struct {
	activityOutbound
	packet *activityPacket
}

func (o mixedActivityOutbound) UDP(string) (UDPConn, error) { return o.packet, nil }

func TestHQIPActivityTCPAndUDPShareUntilLastTargetCloses(t *testing.T) {
	a, f := activityFixture(t)
	l, _, err := a.open(context.Background(), randomConnectionID(), "credential", "192.0.2.1", func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer l.finish()
	p := &activityPacket{}
	o := &leasedOutbound{base: mixedActivityOutbound{activityOutbound{t}, p}, lease: l}
	tcp, err := o.TCP("target:443")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := o.UDP("target:53")
	if err != nil {
		t.Fatal(err)
	}
	_ = tcp.Close()
	if active, _, finals := f.counts(); active != 1 || finals != 0 {
		t.Fatal("TCP close released UDP", active, finals)
	}
	_ = udp.Close()
	_ = udp.Close()
	if active, opened, finals := f.counts(); active != 0 || opened != 1 || finals != 1 || !p.closed.Load() {
		t.Fatal("last UDP did not close exactly once", active, opened, finals)
	}
}

func TestHQIPActivityFailedDialAndTransportCloseRelease(t *testing.T) {
	a, f := activityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var closed atomic.Bool
	l, _, err := a.open(ctx, randomConnectionID(), "credential", "192.0.2.1", func() { closed.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	o := &leasedOutbound{base: activityOutbound{t}, lease: l}
	if _, err = o.UDP("unreachable:53"); err == nil {
		t.Fatal("fixture did not fail dial")
	}
	if active, _, finals := f.counts(); active != 0 || finals != 1 || closed.Load() {
		t.Fatal("failed dial stranded capacity or closed transport", active, finals)
	}
	tcp, err := o.TCP("target:443")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	l.finish()
	_ = tcp.Close() // Late target cleanup cannot finalize twice.
	l.finish()
	if active, opened, finals := f.counts(); active != 0 || opened != 2 || finals != 2 || !closed.Load() {
		t.Fatal("transport close retained capacity", active, opened, finals)
	}
	if _, err = o.TCP("target:443"); !errors.Is(err, net.ErrClosed) {
		t.Fatal("closed transport admitted new work", err)
	}
}

func TestHQIPActivityConcurrentTargetsReserveExactlyOnce(t *testing.T) {
	a, f := activityFixture(t)
	l, _, err := a.open(context.Background(), randomConnectionID(), "credential", "192.0.2.1", func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer l.finish()
	o := &leasedOutbound{base: activityOutbound{t}, lease: l}
	var opened, ended sync.WaitGroup
	closeTargets := make(chan struct{})
	for i := 0; i < 32; i++ {
		opened.Add(1)
		ended.Add(1)
		go func() {
			defer ended.Done()
			c, err := o.TCP("target:443")
			if err != nil {
				t.Error(err)
				opened.Done()
				return
			}
			opened.Done()
			<-closeTargets
			_ = c.Close()
			_ = c.Close()
		}()
	}
	opened.Wait()
	if active, admitted, finals := f.counts(); active != 1 || admitted != 1 || finals != 0 {
		t.Error("parallel targets double-admitted", active, admitted, finals)
	}
	close(closeTargets)
	ended.Wait()
	if active, admitted, finals := f.counts(); active != 0 || admitted != 1 || finals != 1 {
		t.Fatal("parallel close leaked or double-finalized", active, admitted, finals)
	}
}
