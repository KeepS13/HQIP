//go:build windows

package client

import (
	"context"
	"errors"
	"math/bits"
	"net"
	"testing"

	"golang.org/x/sys/windows"
)

// Creating a bound socket sends no traffic and does not change routing. This
// checks the actual Windows socket option, independently of the QUIC fixture.
func TestNodeProbePinsWindowsSocketToPhysicalInterface(t *testing.T) {
	for _, ip := range []string{"192.0.2.1", "2001:db8::1"} {
		t.Run(ip, func(t *testing.T) {
			address := &net.UDPAddr{IP: net.ParseIP(ip), Port: 443}
			packet, err := directProbePacket(context.Background(), address)
			if errors.Is(err, errProbeDirect) {
				t.Skip("no eligible physical default interface for this address family")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer packet.Close()
			raw, err := packet.(*net.UDPConn).SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var value int
			var optionErr error
			level := windows.IPPROTO_IP
			if address.IP.To4() == nil {
				level = windows.IPPROTO_IPV6
			}
			if err = raw.Control(func(fd uintptr) { value, optionErr = windows.GetsockoptInt(windows.Handle(fd), level, 31) }); err != nil {
				t.Fatal(err)
			}
			if optionErr != nil {
				t.Fatal(optionErr)
			}
			// getsockopt returns the interface index in host byte order for both
			// families, unlike the IPv4 setsockopt input.
			index := uint32(value)
			row := windows.MibIfRow2{InterfaceIndex: index}
			if err = windows.GetIfEntry2Ex(windows.MibIfTableNormalWithoutStatistics, &row); err != nil {
				t.Fatalf("invalid bound interface %d (byte-swapped %d): %v", index, bits.ReverseBytes32(index), err)
			}
			if index == 0 || row.InterfaceAndOperStatusFlags&1 == 0 {
				t.Fatalf("socket not bound to hardware: %d", index)
			}
		})
	}
}
