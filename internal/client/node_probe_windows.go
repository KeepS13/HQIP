//go:build windows

package client

import (
	"context"
	"errors"
	"math/bits"
	"net"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func directProbePacket(ctx context.Context, address *net.UDPAddr) (net.PacketConn, error) {
	family, network, listen := uint32(windows.AF_INET), "udp4", "0.0.0.0:0"
	if address.IP.To4() == nil {
		family, network, listen = windows.AF_INET6, "udp6", "[::]:0"
	}
	// Loopback is used only by local protocol regression tests.
	if address.IP.IsLoopback() {
		return (&net.ListenConfig{}).ListenPacket(ctx, network, listen)
	}
	size := uint32(15000)
	var buffer []byte
	loaded := false
	for attempts := 0; attempts < 3; attempts++ {
		buffer = make([]byte, size)
		err := windows.GetAdaptersAddresses(family, windows.GAA_FLAG_INCLUDE_GATEWAYS, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0])), &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue
		}
		if err != nil {
			return nil, errProbeDirect
		}
		loaded = true
		break
	}
	if !loaded {
		return nil, errProbeDirect
	}
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(uint16(family), &table); err != nil {
		return nil, errProbeDirect
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	routeMetrics := map[uint32]uint32{}
	for _, route := range table.Rows() {
		if route.DestinationPrefix.PrefixLength != 0 || route.Loopback != 0 || route.ValidLifetime == 0 {
			continue
		}
		if old, ok := routeMetrics[route.InterfaceIndex]; !ok || route.Metric < old {
			routeMetrics[route.InterfaceIndex] = route.Metric
		}
	}
	var index uint32
	metric := uint64(^uint64(0))
	for adapter := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0])); adapter != nil; adapter = adapter.Next {
		if adapter.OperStatus != windows.IfOperStatusUp || (adapter.IfType != 6 && adapter.IfType != 71) || strings.EqualFold(windows.UTF16PtrToString(adapter.FriendlyName), "HQIP") {
			continue
		}
		hasGateway := false
		for gateway := adapter.FirstGatewayAddress; gateway != nil; gateway = gateway.Next {
			ip := gateway.Address.IP()
			if ip != nil && !ip.IsUnspecified() && (ip.To4() != nil) == (family == windows.AF_INET) {
				hasGateway = true
			}
		}
		if !hasGateway {
			continue
		}
		candidate, cost := adapter.IfIndex, adapter.Ipv4Metric
		if family == windows.AF_INET6 {
			candidate, cost = adapter.Ipv6IfIndex, adapter.Ipv6Metric
		}
		row := windows.MibIfRow2{InterfaceLuid: adapter.Luid}
		// HardwareInterface excludes TAP/virtual Ethernet adapters that could
		// route the measurement through another tunnel.
		if windows.GetIfEntry2Ex(windows.MibIfTableNormalWithoutStatistics, &row) != nil || row.InterfaceAndOperStatusFlags&1 == 0 {
			continue
		}
		routeCost, ok := routeMetrics[candidate]
		totalCost := uint64(cost) + uint64(routeCost)
		if ok && candidate != 0 && (index == 0 || totalCost < metric) {
			index, metric = candidate, totalCost
		}
	}
	if index == 0 {
		return nil, errProbeDirect
	}
	config := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var optionErr error
		err := raw.Control(func(fd uintptr) {
			level, value := windows.IPPROTO_IPV6, int(index)
			if family == windows.AF_INET {
				level, value = windows.IPPROTO_IP, int(bits.ReverseBytes32(index))
			}
			// IP_UNICAST_IF expects network byte order; IPV6_UNICAST_IF uses host order.
			optionErr = windows.SetsockoptInt(windows.Handle(fd), level, 31, value)
		})
		if err != nil {
			return err
		}
		return optionErr
	}}
	packet, err := config.ListenPacket(ctx, network, listen)
	if err != nil {
		return nil, errProbeDirect
	}
	return packet, nil
}
