//go:build android

package mobile

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/sagernet/sing-box/experimental/libbox"
)

type nativePlatform struct {
	callbacks Callbacks
	mu        sync.Mutex
	monitor   libbox.InterfaceUpdateListener
}
type networkInfo struct {
	Name      string   `json:"name"`
	Index     int32    `json:"index"`
	MTU       int32    `json:"mtu"`
	Addresses []string `json:"addresses"`
	DNS       []string `json:"dns"`
	Default   bool     `json:"default"`
	Metered   bool     `json:"metered"`
}
type stringIterator struct {
	values []string
	index  int
}

func (i *stringIterator) HasNext() bool { return i.index < len(i.values) }
func (i *stringIterator) Next() string  { v := i.values[i.index]; i.index++; return v }
func (i *stringIterator) Len() int32    { return int32(len(i.values) - i.index) }

type interfaceIterator struct {
	values []*libbox.NetworkInterface
	index  int
}

func (i *interfaceIterator) HasNext() bool { return i.index < len(i.values) }
func (i *interfaceIterator) Next() *libbox.NetworkInterface {
	v := i.values[i.index]
	i.index++
	return v
}

func (p *nativePlatform) networks() ([]networkInfo, error) {
	raw, err := p.callbacks.Interfaces()
	if err != nil {
		return nil, err
	}
	var result []networkInfo
	err = json.Unmarshal([]byte(raw), &result)
	return result, err
}
func (p *nativePlatform) GetInterfaces() (libbox.NetworkInterfaceIterator, error) {
	networks, err := p.networks()
	if err != nil {
		return nil, err
	}
	result := &interfaceIterator{}
	for _, n := range networks {
		result.values = append(result.values, &libbox.NetworkInterface{Index: n.Index, Name: n.Name, MTU: n.MTU, Addresses: &stringIterator{values: n.Addresses}, Flags: uint32Flags(), DNSServer: &stringIterator{values: n.DNS}, Type: libbox.InterfaceTypeOther, Metered: n.Metered})
	}
	return result, nil
}
func uint32Flags() int32 { return int32(net.FlagUp | net.FlagRunning | net.FlagMulticast) }
func (p *nativePlatform) StartDefaultInterfaceMonitor(listener libbox.InterfaceUpdateListener) error {
	p.mu.Lock()
	p.monitor = listener
	p.mu.Unlock()
	p.updateMonitor()
	return nil
}
func (p *nativePlatform) CloseDefaultInterfaceMonitor(libbox.InterfaceUpdateListener) error {
	p.mu.Lock()
	p.monitor = nil
	p.mu.Unlock()
	return nil
}
func (p *nativePlatform) updateMonitor() {
	p.mu.Lock()
	listener := p.monitor
	p.mu.Unlock()
	if listener == nil {
		return
	}
	networks, err := p.networks()
	if err == nil {
		for _, n := range networks {
			if n.Default {
				listener.UpdateDefaultInterface(n.Name, n.Index, n.Metered, false)
				return
			}
		}
	}
	listener.UpdateDefaultInterface("", -1, false, false)
}
func (*nativePlatform) UsePlatformAutoDetectInterfaceControl() bool { return true }
func (p *nativePlatform) AutoDetectInterfaceControl(fd int32) error { return p.callbacks.Protect(fd) }
func (p *nativePlatform) OpenTun(options libbox.TunOptions) (int32, error) {
	addresses, routes := []string{}, []string{}
	for _, iterator := range []libbox.RoutePrefixIterator{options.GetInet4Address(), options.GetInet6Address()} {
		for iterator.HasNext() {
			addresses = append(addresses, iterator.Next().String())
		}
	}
	for _, iterator := range []libbox.RoutePrefixIterator{options.GetInet4RouteRange(), options.GetInet6RouteRange()} {
		for iterator.HasNext() {
			routes = append(routes, iterator.Next().String())
		}
	}
	dns, err := options.GetDNSServerAddress()
	if err != nil {
		return -1, err
	}
	servers := []string{}
	for dns.HasNext() {
		servers = append(servers, dns.Next())
	}
	raw, _ := json.Marshal(map[string]any{"addresses": addresses, "routes": routes, "dns": servers, "mtu": options.GetMTU()})
	return p.callbacks.OpenTun(string(raw))
}

type nativeDNS struct{ callbacks Callbacks }

func (p *nativePlatform) LocalDNSTransport() libbox.LocalDNSTransport { return &nativeDNS{p.callbacks} }
func (*nativeDNS) Raw() bool                                          { return false }
func (d *nativeDNS) Lookup(ctx *libbox.ExchangeContext, network, domain string) error {
	addresses, err := d.callbacks.Lookup(domain)
	if err != nil {
		return err
	}
	result := []string{}
	for _, address := range strings.Split(addresses, "\n") {
		ip := net.ParseIP(address)
		if ip != nil && (network == "ip" || (network == "ip4") == (ip.To4() != nil)) {
			result = append(result, address)
		}
	}
	ctx.Success(strings.Join(result, "\n"))
	return nil
}
func (*nativeDNS) Exchange(*libbox.ExchangeContext, []byte) error {
	return errors.New("raw DNS not supported")
}

// Optional native features are explicitly disabled. HQIP does not request Wi-Fi
// location, SSH/shell, neighbor scans or bridge privileges for a VPN client.
func (*nativePlatform) UnderNetworkExtension() bool      { return false }
func (*nativePlatform) IncludeAllNetworks() bool         { return false }
func (*nativePlatform) ReadWIFIState() *libbox.WIFIState { return nil }
func (*nativePlatform) ClearDNSCache()                   {}
func (*nativePlatform) RegisterMyInterface(string)       {}
func (*nativePlatform) FindConnectionOwner(int32, string, int32, string, int32) (*libbox.ConnectionOwner, error) {
	return nil, errors.New("process matching unavailable")
}
func (*nativePlatform) SendNotification(*libbox.Notification) error { return nil }
func (*nativePlatform) CancelNotification(string, int32) error      { return nil }
func (*nativePlatform) StartNeighborMonitor(libbox.NeighborUpdateListener) error {
	return errors.New("neighbor monitoring unavailable")
}
func (*nativePlatform) CloseNeighborMonitor(libbox.NeighborUpdateListener) error { return nil }
func (*nativePlatform) UsePlatformShell() bool                                   { return false }
func (*nativePlatform) CheckPlatformShell() error                                { return errors.New("shell unavailable") }
func (*nativePlatform) OpenShellSession(*libbox.PlatformUser, string, libbox.StringIterator, string, int32, int32) (libbox.ShellSession, error) {
	return nil, errors.New("shell unavailable")
}
func (*nativePlatform) LookupUser(string) (*libbox.PlatformUser, error) {
	return nil, errors.New("shell unavailable")
}
func (*nativePlatform) LookupSFTPServer() (string, error) { return "", errors.New("SFTP unavailable") }
func (*nativePlatform) ReadSystemSSHHostKey() (string, error) {
	return "", errors.New("SSH unavailable")
}
func (*nativePlatform) TailscaleHostname() string { return "" }
func (*nativePlatform) UsePlatformBridge() bool   { return false }
func (*nativePlatform) CreateBridge(*libbox.BridgeOptions) (libbox.BridgeSession, error) {
	return nil, errors.New("bridge unavailable")
}
func (*nativePlatform) ServiceStop() error   { return nil }
func (*nativePlatform) ServiceReload() error { return errors.New("reload through HQIP controller") }
func (*nativePlatform) GetSystemProxyStatus() (*libbox.SystemProxyStatus, error) {
	return &libbox.SystemProxyStatus{}, nil
}
func (*nativePlatform) SetSystemProxyEnabled(bool) error { return errors.New("use VPN service") }
func (*nativePlatform) TriggerNativeCrash() error        { return errors.New("disabled") }
func (*nativePlatform) WriteDebugMessage(string)         {}
func (*nativePlatform) ConnectSSHAgent() (int32, error)  { return -1, errors.New("SSH unavailable") }
