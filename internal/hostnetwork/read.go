// Package hostnetwork reads host NIC counters independently of proxy processes.
// It never writes network settings or combines virtual and physical forwarding
// layers. Driver counters are not a promise of equivalence to provider billing.
package hostnetwork

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var ErrUnsupported = errors.New("host network counters unavailable on this platform")

// Read returns one caller-owned snapshot or an error; an unreadable interface
// invalidates the whole sample instead of quietly reporting a partial zero.
func Read(now time.Time) (model.HostNetworkSample, error) {
	if runtime.GOOS != "linux" {
		return model.HostNetworkSample{}, ErrUnsupported
	}
	return readAt("/proc", "/sys/class/net", now)
}

func readAt(proc, network string, now time.Time) (model.HostNetworkSample, error) {
	s := model.HostNetworkSample{At: now.UTC(), Interfaces: []model.HostNetworkInterface{}}
	boot, err := os.ReadFile(filepath.Join(proc, "sys/kernel/random/boot_id"))
	if err != nil {
		return s, err
	}
	s.BootID = strings.TrimSpace(string(boot))
	stat, err := os.ReadFile(filepath.Join(proc, "stat"))
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(string(stat), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			stamp, e := strconv.ParseInt(fields[1], 10, 64)
			if e != nil || stamp <= 0 {
				return s, errors.New("invalid boot time")
			}
			s.BootAt = time.Unix(stamp, 0).UTC()
		}
	}
	if s.BootAt.IsZero() || s.BootAt.After(now) || len(s.BootID) != 36 {
		return s, errors.New("invalid host boot identity")
	}
	data, err := os.ReadFile(filepath.Join(proc, "net/dev"))
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, values, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, `/\\`) {
			return s, errors.New("invalid interface name")
		}
		// Virtio/hypervisor NICs have a device link too. Bond/bridge/VLAN/tunnel
		// masters and veth are excluded: count their physical carrier only once.
		if _, e := os.Stat(filepath.Join(network, name, "device")); e != nil {
			if !os.IsNotExist(e) {
				return s, e
			}
			s.Excluded = append(s.Excluded, name)
			continue
		}
		fields := strings.Fields(values)
		if len(fields) != 16 {
			return s, fmt.Errorf("invalid interface statistics: %s", name)
		}
		rx, e1 := strconv.ParseInt(fields[0], 10, 64)
		tx, e2 := strconv.ParseInt(fields[8], 10, 64)
		index, e3 := os.ReadFile(filepath.Join(network, name, "ifindex"))
		id, e4 := strconv.Atoi(strings.TrimSpace(string(index)))
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || id <= 0 || rx < 0 || tx < 0 || rx > 1<<60 || tx > 1<<60 {
			return s, fmt.Errorf("invalid interface counters: %s", name)
		}
		s.Interfaces = append(s.Interfaces, model.HostNetworkInterface{Name: name, Index: id, Receive: rx, Send: tx})
	}
	if len(s.Interfaces) == 0 || len(s.Interfaces) > 64 || len(s.Excluded) > 256 {
		return s, errors.New("no supported host NIC or too many interfaces")
	}
	sort.Slice(s.Interfaces, func(i, j int) bool { return s.Interfaces[i].Index < s.Interfaces[j].Index })
	sort.Strings(s.Excluded)
	return s, nil
}
