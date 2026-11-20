//go:build windows

package winclient

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestOwnedProxyBypassAndSnapshotRestore(t *testing.T) {
	// Exercise the native registry value encoding under an isolated test key.
	// Never change the user's real Internet Settings or start a proxy watcher.
	path := fmt.Sprintf(`Software\HQIP-ProxyTest-%d-%d`, os.Getpid(), time.Now().UnixNano())
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		key.Close()
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil {
			t.Error("remove isolated proxy test key:", err)
		}
	})
	before := map[string]proxyValue{
		"ProxyEnable":   {Present: true, Kind: registry.DWORD, Number: 0},
		"ProxyServer":   {Present: true, Kind: registry.SZ, Text: "custom.example.test:8080"},
		"ProxyOverride": {Present: true, Kind: registry.SZ, Text: "<local>;intranet.example.test"},
		"AutoConfigURL": {Present: true, Kind: registry.SZ, Text: "https://config.example.test/proxy.pac"},
	}
	if err := writeProxy(key, before); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	manager := NewProxyManager(dir)
	lease := proxyLease{PID: os.Getpid(), Stamp: 1, Before: before, Owned: ownedProxyValues(14001), Committed: true}
	if err := manager.save(lease); err != nil {
		t.Fatal(err)
	}
	if err := writeProxy(key, lease.Owned); err != nil {
		t.Fatal(err)
	}
	got, err := readProxy(key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(strings.Split(got["ProxyOverride"].Text, ";"), []string{"<local>", "hqip.sharevps.online"}) {
		t.Fatal("system proxy bypass contains retired or unexpected domains")
	}
	if got["ProxyEnable"].Number != 1 || got["ProxyServer"].Text != "127.0.0.1:14001" || got["AutoConfigURL"].Present {
		t.Fatal("owned system proxy settings were not applied")
	}
	recovered, err := manager.load()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProxy(key, recovered.Before); err != nil {
		t.Fatal(err)
	}
	got, err = readProxy(key)
	if err != nil || !reflect.DeepEqual(got, before) {
		t.Fatal("prior custom proxy, bypass or PAC settings did not survive snapshot restoration", err)
	}
}
