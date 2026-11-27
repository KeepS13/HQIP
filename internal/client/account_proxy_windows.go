//go:build windows

package client

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func accountTLSFallback(host, port string, config *tls.Config) accountTLSDial {
	if !previewAccountProxyFallback.Load() {
		return nil
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled != 1 {
		return nil
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		return nil
	}
	bypass, _, _ := key.GetStringValue("ProxyOverride")
	proxy := previewLoopbackProxy(server, bypass, host)
	if proxy == "" || !externalProxyListener(proxy) {
		return nil
	}
	return func(ctx context.Context) (net.Conn, error) {
		// The configured endpoint may have been replaced during direct dialing.
		if !externalProxyListener(proxy) {
			return nil, errProbeDirect
		}
		return dialAccountProxyTLS(ctx, proxy, net.JoinHostPort(host, port), config)
	}
}

// HQIP's mixed ports are dynamically allocated. Resolve the actual listener
// owner instead of relying on a fixed port or routing account recovery through
// our own engine. Unknown/inaccessible owners fail closed.
func externalProxyListener(address string) bool {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(rawPort)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return false
	}
	family, rowSize, portOffset, pidOffset := uintptr(windows.AF_INET), 24, 8, 20
	if ip.To4() == nil {
		family, rowSize, portOffset, pidOffset = windows.AF_INET6, 56, 20, 52
	}
	proc := windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	size := uint32(0)
	result, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, family, 3, 0) // TCP_TABLE_OWNER_PID_LISTENER
	if result != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size < 4 || size > 16<<20 {
		return false
	}
	for attempt := 0; attempt < 3; attempt++ {
		buffer := make([]byte, size)
		result, _, _ = proc.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, family, 3, 0)
		if result == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) && size <= 16<<20 {
			continue
		}
		if result != 0 {
			return false
		}
		count := int(binary.LittleEndian.Uint32(buffer[:4]))
		if count > (len(buffer)-4)/rowSize {
			return false
		}
		for i := 0; i < count; i++ {
			row := buffer[4+i*rowSize : 4+(i+1)*rowSize]
			if int(binary.BigEndian.Uint16(row[portOffset:portOffset+2])) != port {
				continue
			}
			local := net.IP(row[4:8])
			if family == windows.AF_INET6 {
				local = net.IP(row[:16])
			}
			if !local.Equal(ip) && !local.IsUnspecified() {
				continue
			}
			pid := binary.LittleEndian.Uint32(row[pidOffset : pidOffset+4])
			if pid == uint32(os.Getpid()) || pid == 0 {
				return false
			}
			process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err != nil {
				return false
			}
			name := make([]uint16, 32768)
			length := uint32(len(name))
			err = windows.QueryFullProcessImageName(process, 0, &name[0], &length)
			windows.CloseHandle(process)
			if err != nil {
				return false
			}
			return externalProxyExecutable(windows.UTF16ToString(name[:length]))
		}
		return false
	}
	return false
}

func externalProxyExecutable(executable string) bool {
	name := strings.ToLower(filepath.Base(executable))
	return !strings.HasPrefix(name, "hqip") && name != "sing-box.exe"
}
