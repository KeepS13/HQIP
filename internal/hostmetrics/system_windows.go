//go:build windows

package hostmetrics

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var kernel = windows.NewLazySystemDLL("kernel32.dll")
var getSystemTimes = kernel.NewProc("GetSystemTimes")
var globalMemoryStatus = kernel.NewProc("GlobalMemoryStatusEx")

type memoryStatus struct {
	Length, Load                                                                                         uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile, TotalVirtual, AvailVirtual, AvailExtendedVirtual uint64
}

func readSystem(dataDir string) (*cpuTimes, *Usage, *Usage) {
	var times *cpuTimes
	var idle, system, user windows.Filetime
	if ok, _, _ := getSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&system)), uintptr(unsafe.Pointer(&user))); ok != 0 {
		ticks := func(t windows.Filetime) uint64 { return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime) }
		times = &cpuTimes{total: ticks(system) + ticks(user), idle: ticks(idle)}
	}
	var memory, disk *Usage
	m := memoryStatus{}
	m.Length = uint32(unsafe.Sizeof(m))
	if ok, _, _ := globalMemoryStatus.Call(uintptr(unsafe.Pointer(&m))); ok != 0 && m.TotalPhys > 0 && m.AvailPhys <= m.TotalPhys {
		memory = &Usage{Used: m.TotalPhys - m.AvailPhys, Total: m.TotalPhys}
	}
	if dir, err := windows.UTF16PtrFromString(dataDir); err == nil {
		var available, total, free uint64
		if windows.GetDiskFreeSpaceEx(dir, &available, &total, &free) == nil && total > 0 && free <= total {
			disk = &Usage{Used: total - free, Total: total}
		}
	}
	return times, memory, disk
}
