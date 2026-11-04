//go:build windows

package winclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type proxyValue struct {
	Present bool
	Kind    uint32
	Text    string
	Number  uint64
}
type proxyLease struct {
	PID           int
	Stamp         int64
	Before, Owned map[string]proxyValue
	Committed     bool
}
type ProxyManager struct{ dir, helperPath string }

func NewProxyManager(dir string) *ProxyManager { return &ProxyManager{dir: dir} }

const internetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

var proxyKeys = []string{"ProxyEnable", "ProxyServer", "ProxyOverride", "AutoConfigURL"}

func (p *ProxyManager) locked(fn func() error) error {
	sid, err := currentSID()
	if err != nil {
		return err
	}
	name, _ := windows.UTF16PtrFromString(`Local\HQIP-Proxy-` + sid)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	w, err := windows.WaitForSingleObject(h, 10000)
	if err != nil {
		return err
	}
	if w != windows.WAIT_OBJECT_0 && w != windows.WAIT_ABANDONED {
		return errors.New("系统代理正由其他进程更新")
	}
	defer windows.ReleaseMutex(h)
	return fn()
}
func readProxy(k registry.Key) (map[string]proxyValue, error) {
	out := map[string]proxyValue{}
	for _, name := range proxyKeys {
		_, kind, err := k.GetValue(name, nil)
		if errors.Is(err, registry.ErrNotExist) {
			out[name] = proxyValue{}
			continue
		}
		if err != nil {
			return nil, err
		}
		v := proxyValue{Present: true, Kind: kind}
		switch kind {
		case registry.SZ, registry.EXPAND_SZ:
			v.Text, _, err = k.GetStringValue(name)
		case registry.DWORD:
			v.Number, _, err = k.GetIntegerValue(name)
		default:
			return nil, errors.New("无法安全保存现有系统代理设置")
		}
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}
func writeProxy(k registry.Key, values map[string]proxyValue) error {
	for _, name := range proxyKeys {
		v := values[name]
		var err error
		if !v.Present {
			err = k.DeleteValue(name)
			if errors.Is(err, registry.ErrNotExist) {
				err = nil
			}
		} else {
			switch v.Kind {
			case registry.SZ:
				err = k.SetStringValue(name, v.Text)
			case registry.EXPAND_SZ:
				err = k.SetExpandStringValue(name, v.Text)
			case registry.DWORD:
				err = k.SetDWordValue(name, uint32(v.Number))
			default:
				return errors.New("代理设置类型无效")
			}
		}
		if err != nil {
			return err
		}
	}
	proc := windows.NewLazySystemDLL("wininet.dll").NewProc("InternetSetOptionW")
	for _, option := range []uintptr{39, 37} {
		r, _, err := proc.Call(0, option, 0, 0)
		if r == 0 {
			return err
		}
	}
	return nil
}
func (p *ProxyManager) save(lease proxyLease) error {
	b, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	b, err = Protect(b)
	if err != nil {
		return err
	}
	path := filepath.Join(p.dir, "proxy.lease")
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	src, _ := windows.UTF16PtrFromString(tmp)
	dst, _ := windows.UTF16PtrFromString(path)
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func (p *ProxyManager) load() (proxyLease, error) {
	var v proxyLease
	b, err := os.ReadFile(filepath.Join(p.dir, "proxy.lease"))
	if err != nil {
		return v, err
	}
	b, err = Unprotect(b)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (p *ProxyManager) restoreLocked(expectedPID int, expectedStamp int64) error {
	lease, err := p.load()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if expectedPID != 0 && (lease.PID != expectedPID || lease.Stamp != expectedStamp) {
		return nil
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	current, err := readProxy(k)
	if err != nil {
		return err
	}
	owned := reflect.DeepEqual(current, lease.Owned)
	if !lease.Committed {
		owned = true
		for _, name := range proxyKeys {
			if current[name] != lease.Owned[name] && current[name] != lease.Before[name] {
				owned = false
			}
		}
	}
	if owned {
		if err = writeProxy(k, lease.Before); err != nil {
			return err
		}
	}
	return os.Remove(filepath.Join(p.dir, "proxy.lease"))
}
func (p *ProxyManager) Recover() error {
	return p.locked(func() error {
		v, err := p.load()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		stamp, err := processStamp(v.PID)
		if err == nil && stamp == v.Stamp {
			return nil
		}
		return p.restoreLocked(v.PID, v.Stamp)
	})
}
func (p *ProxyManager) Restore() error {
	return p.locked(func() error {
		stamp, err := processStamp(os.Getpid())
		if err != nil {
			return err
		}
		return p.restoreLocked(os.Getpid(), stamp)
	})
}
func (p *ProxyManager) Enable(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("系统代理端口无效")
	}
	return p.locked(func() error {
		stamp, err := processStamp(os.Getpid())
		if err != nil {
			return err
		}
		old, err := p.load()
		if err == nil {
			owner, e := processStamp(old.PID)
			if e == nil && owner == old.Stamp && old.PID != os.Getpid() {
				return errors.New("另一个 HQIP 客户端正在使用系统代理")
			}
			if err = p.restoreLocked(old.PID, old.Stamp); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		before, err := readProxy(k)
		if err != nil {
			return err
		}
		lease := proxyLease{PID: os.Getpid(), Stamp: stamp, Before: before, Owned: map[string]proxyValue{"ProxyEnable": {Present: true, Kind: registry.DWORD, Number: 1}, "ProxyServer": {Present: true, Kind: registry.SZ, Text: fmt.Sprintf("127.0.0.1:%d", port)}, "ProxyOverride": {Present: true, Kind: registry.SZ, Text: "<local>;api-hqip.5202345.xyz;hqip.5202345.xyz"}, "AutoConfigURL": {}}}
		if err = p.save(lease); err != nil {
			return err
		}
		watch := exec.Command(p.helperPath, "--watch-proxy", strconv.Itoa(os.Getpid()), strconv.FormatInt(stamp, 10), p.dir)
		HideCommand(watch)
		if err = watch.Start(); err != nil {
			_ = os.Remove(filepath.Join(p.dir, "proxy.lease"))
			return errors.New("无法启动系统代理恢复监视进程")
		}
		_ = watch.Process.Release()
		if err = writeProxy(k, lease.Owned); err != nil {
			_ = p.restoreLocked(lease.PID, lease.Stamp)
			return err
		}
		lease.Committed = true
		if err = p.save(lease); err != nil {
			_ = p.restoreLocked(lease.PID, lease.Stamp)
			return err
		}
		return nil
	})
}
func watchProxy(pid int, stamp int64, dir string) error {
	actual, err := processStamp(pid)
	if err == nil && actual == stamp {
		h, e := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if e == nil {
			_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
			windows.CloseHandle(h)
		}
	}
	p := NewProxyManager(dir)
	return p.locked(func() error { return p.restoreLocked(pid, stamp) })
}
