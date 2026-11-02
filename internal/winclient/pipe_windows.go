//go:build windows

package winclient

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const serviceName = "HQIPHelper"

func pipeName(sid string) string { return `\\.\pipe\HQIPHelper-` + sid }
func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxMessage {
		return errors.New("服务请求过大")
	}
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(b)))
	if _, err = w.Write(size[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
func readFrame(r io.Reader, v any) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := binary.LittleEndian.Uint32(size[:])
	if n == 0 || n > maxMessage {
		return errors.New("服务消息大小无效")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return strictJSON(data, v)
}
func strictJSON(data []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("多余的服务消息")
	}
	return nil
}

func installedDir() (string, error) {
	p, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	return filepath.Join(p, "HQIP Helper"), err
}
func pipeClient(sid string) (*os.File, error) {
	name, _ := windows.UTF16PtrFromString(pipeName(sid))
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return nil, err
	}
	var pid uint32
	if err = windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	// Authenticate the server before transmitting credentials: a same-name pipe
	// created by another user process must not receive privileged configuration.
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	defer windows.CloseHandle(p)
	servicePID, err := trustedServicePID()
	if err != nil || servicePID != pid {
		windows.CloseHandle(h)
		return nil, errors.New("TUN 服务身份验证失败")
	}
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err = windows.QueryFullProcessImageName(p, 0, &buf[0], &size); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	dir, err := installedDir()
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if !strings.EqualFold(windows.UTF16ToString(buf[:size]), filepath.Join(dir, "hqip-helper.exe")) {
		windows.CloseHandle(h)
		return nil, errors.New("TUN 服务路径验证失败")
	}
	return os.NewFile(uintptr(h), pipeName(sid)), nil
}

func trustedServicePID() (uint32, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, err
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(serviceName)
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0, err
	}
	defer windows.CloseServiceHandle(s)
	var status windows.SERVICE_STATUS_PROCESS
	var needed uint32
	err = windows.QueryServiceStatusEx(s, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed)
	return status.ProcessId, err
}

func (a *Adapter) startTUN(ctx context.Context, config []byte) (func() error, error) {
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	// The unprivileged client reads its own rule file; the service never opens a
	// client-supplied path as SYSTEM. It receives bytes and writes its private copy.
	var raw struct {
		Route struct {
			RuleSet []struct {
				Path string `json:"path"`
			} `json:"rule_set"`
		} `json:"route"`
	}
	if err = json.Unmarshal(config, &raw); err != nil || len(raw.Route.RuleSet) > 1 {
		return nil, errors.New("GeoIP 规则路径无效")
	}
	var geo []byte
	if len(raw.Route.RuleSet) == 1 {
		geoPath, err := filepath.EvalSymlinks(raw.Route.RuleSet[0].Path)
		if err != nil {
			return nil, err
		}
		base, err := filepath.EvalSymlinks(a.stateDir)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(base, geoPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Base(geoPath) != "geoip-cn.srs" {
			return nil, errors.New("GeoIP 文件必须位于客户端私有目录")
		}
		geo, err = os.ReadFile(geoPath)
		if err != nil {
			return nil, err
		}
		if len(geo) > 8<<20 {
			return nil, errors.New("GeoIP 文件过大")
		}
	}
	if _, err = validateTUN(config, "geoip-cn.srs"); err != nil {
		return nil, err
	}
	pipe, err := pipeClient(sid)
	if err != nil {
		helper := filepath.Join(filepath.Dir(a.corePath), "hqip-helper.exe")
		verb, _ := windows.UTF16PtrFromString("runas")
		file, _ := windows.UTF16PtrFromString(helper)
		args, _ := windows.UTF16PtrFromString("--install --sid " + syscall.EscapeArg(sid) + " --core " + syscall.EscapeArg(a.corePath))
		if err = windows.ShellExecute(0, verb, file, args, nil, windows.SW_HIDE); err != nil {
			return nil, errors.New("首次 TUN 服务安装未完成，请允许 Windows 管理员授权")
		}
		deadline := time.NewTimer(90 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for pipe == nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-deadline.C:
				return nil, errors.New("等待 TUN 服务安装超时")
			case <-tick.C:
				pipe, _ = pipeClient(sid)
			}
		}
	}
	failed := true
	defer func() {
		if failed {
			pipe.Close()
		}
	}()
	if err = writeFrame(pipe, helperRequest{Command: "start", Config: config, Geo: geo}); err != nil {
		return nil, err
	}
	response := make(chan error, 1)
	go func() {
		var res helperResponse
		if err := readFrame(pipe, &res); err != nil {
			response <- err
		} else if !res.OK {
			response <- errors.New(res.Error)
		} else {
			response <- nil
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, errors.New("TUN 服务启动超时")
	case err = <-response:
		if err != nil {
			return nil, err
		}
	}
	failed = false
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() {
			defer pipe.Close()
			if e := writeFrame(pipe, helperRequest{Command: "stop"}); e != nil {
				return
			}
			ack := make(chan error, 1)
			go func() { var res helperResponse; ack <- readFrame(pipe, &res) }()
			select {
			case <-ack:
			case <-time.After(7 * time.Second):
				stopErr = errors.New("TUN 停止确认超时，连接已关闭")
			}
		})
		return stopErr
	}
	return stop, nil
}
