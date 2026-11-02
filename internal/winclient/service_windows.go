//go:build windows

package winclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const privateSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
const installSDDL = privateSDDL + "(A;OICI;GRGX;;;BU)"

func HelperMain(args []string) error {
	if len(args) > 0 && args[0] == "--watch-proxy" {
		if len(args) != 4 {
			return errors.New("invalid watchdog arguments")
		}
		pid, e := strconv.Atoi(args[1])
		if e != nil {
			return e
		}
		stamp, e := strconv.ParseInt(args[2], 10, 64)
		if e != nil {
			return e
		}
		return watchProxy(pid, stamp, args[3])
	}
	if len(args) > 0 && args[0] == "--install" {
		f := flag.NewFlagSet("install", flag.ContinueOnError)
		f.SetOutput(io.Discard)
		sid := f.String("sid", "", "")
		core := f.String("core", "", "")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		return installService(*sid, *core)
	}
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return errors.New("helper accepts only install or watchdog modes outside SCM")
	}
	dir, err := installedDir()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "owner.sid"))
	if err != nil {
		return err
	}
	sid := strings.TrimSpace(string(raw))
	if _, err = windows.StringToSid(sid); err != nil {
		return err
	}
	return svc.Run(serviceName, &helperService{sid: sid, dir: dir, pipes: map[windows.Handle]*os.File{}})
}

func verifiedCore(path string) ([]byte, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != coreSHA256 {
		return nil, errors.New("内核校验失败")
	}
	return b, nil
}
func installService(sid, sourceCore string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("服务安装需要管理员权限")
	}
	if _, e := windows.StringToSid(sid); e != nil {
		return e
	}
	core, e := verifiedCore(sourceCore)
	if e != nil {
		return e
	}
	source, e := os.Executable()
	if e != nil {
		return e
	}
	binary, e := os.ReadFile(source)
	if e != nil {
		return e
	}
	dir, e := installedDir()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	if info, e := os.Lstat(dir); e != nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("安装目录不能是链接")
	}
	if e = securePath(dir, installSDDL); e != nil {
		return e
	}
	manager, e := mgr.Connect()
	if e != nil {
		return e
	}
	defer manager.Disconnect()
	existing, e := manager.OpenService(serviceName)
	if e == nil {
		status, _ := existing.Query()
		if status.State != svc.Stopped {
			_, _ = existing.Control(svc.Stop)
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				status, _ = existing.Query()
				if status.State == svc.Stopped {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if status.State != svc.Stopped {
				existing.Close()
				return errors.New("旧 TUN 服务未停止")
			}
		}
		existing.Close()
	}
	for name, data := range map[string][]byte{"hqip-helper.exe": binary, "sing-box.exe": core, "owner.sid": []byte(sid)} {
		path := filepath.Join(dir, name)
		if e = os.WriteFile(path+".new", data, 0600); e != nil {
			return e
		}
		if e = securePath(path+".new", installSDDL); e != nil {
			return e
		}
		src, _ := windows.UTF16PtrFromString(path + ".new")
		dst, _ := windows.UTF16PtrFromString(path)
		if e = windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); e != nil {
			return e
		}
	}
	s, e := manager.OpenService(serviceName)
	if e != nil {
		s, e = manager.CreateService(serviceName, filepath.Join(dir, "hqip-helper.exe"), mgr.Config{DisplayName: "HQIP TUN Helper", Description: "HQIP restricted TUN core service", StartType: mgr.StartAutomatic}, "--service")
		if e != nil {
			return e
		}
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}}, 3600)
	return s.Start()
}

type helperService struct {
	sid, dir string
	mu       sync.Mutex
	pipes    map[windows.Handle]*os.File
	active   bool
}

func (s *helperService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.listen(ctx) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case <-done:
			s.closePipes()
			return false, 1
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				s.closePipes()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
				return false, 0
			}
		}
	}
}
func (s *helperService) closePipes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, pipe := range s.pipes {
		_ = windows.CancelIoEx(h, nil)
		_ = pipe.Close()
		delete(s.pipes, h)
	}
}
func (s *helperService) listen(ctx context.Context) error {
	sd, e := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;" + s.sid + ")")
	if e != nil {
		return e
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, _ := windows.UTF16PtrFromString(pipeName(s.sid))
	first := true
	for ctx.Err() == nil {
		flags := uint32(windows.PIPE_ACCESS_DUPLEX)
		if first {
			flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
		}
		h, e := windows.CreateNamedPipe(name, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 8, 64<<10, 64<<10, 1000, &sa)
		if e != nil {
			if e == windows.ERROR_PIPE_BUSY {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(200 * time.Millisecond):
					continue
				}
			}
			return e
		}
		first = false
		pipe := os.NewFile(uintptr(h), "HQIP service pipe")
		s.mu.Lock()
		s.pipes[h] = pipe
		s.mu.Unlock()
		e = windows.ConnectNamedPipe(h, nil)
		if e != nil && e != windows.ERROR_PIPE_CONNECTED {
			s.dropPipe(h, pipe)
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		go s.handle(ctx, h, pipe)
	}
	return nil
}
func (s *helperService) dropPipe(h windows.Handle, pipe *os.File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pipe.Close()
	if s.pipes[h] == pipe {
		delete(s.pipes, h)
	}
}
func (s *helperService) handle(ctx context.Context, h windows.Handle, pipe *os.File) {
	defer s.dropPipe(h, pipe)
	var pid uint32
	if windows.GetNamedPipeClientProcessId(h, &pid) != nil {
		return
	}
	parent, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if e != nil {
		return
	}
	defer windows.CloseHandle(parent)
	var token windows.Token
	if windows.OpenProcessToken(parent, windows.TOKEN_QUERY, &token) != nil {
		return
	}
	user, e := token.GetTokenUser()
	token.Close()
	if e != nil || user.User.Sid.String() != s.sid {
		return
	}
	// Parent exit revokes the lease even if a duplicated pipe remains open.
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		for {
			select {
			case <-closed:
				return
			case <-ctx.Done():
				pipe.Close()
				return
			case <-time.After(time.Second):
				state, e := windows.WaitForSingleObject(parent, 0)
				if e != nil || state != uint32(windows.WAIT_TIMEOUT) {
					pipe.Close()
					return
				}
			}
		}
	}()
	var req helperRequest
	initialTimeout := time.AfterFunc(15*time.Second, func() { pipe.Close() })
	if e = readFrame(pipe, &req); e != nil {
		initialTimeout.Stop()
		return
	}
	initialTimeout.Stop()
	s.mu.Lock()
	running := s.active
	s.mu.Unlock()
	if req.Command == "status" {
		_ = writeFrame(pipe, helperResponse{OK: true, Running: running})
		return
	}
	if req.Command != "start" {
		_ = writeFrame(pipe, helperResponse{Error: "需要有效的 start 请求"})
		return
	}
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		_ = writeFrame(pipe, helperResponse{Error: "另一个客户端正在使用 TUN"})
		return
	}
	s.active = true
	s.mu.Unlock()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { s.mu.Lock(); s.active = false; s.mu.Unlock() }) }
	defer release()
	if len(req.Geo) > 8<<20 {
		_ = writeFrame(pipe, helperResponse{Error: "GeoIP 规则大小无效"})
		return
	}
	// The install directory is protected from unprivileged replacement. Avoid
	// following an attacker-created ProgramData junction as LocalSystem.
	state := filepath.Join(s.dir, "state")
	if e = os.MkdirAll(state, 0700); e != nil {
		return
	}
	if e = securePath(state, privateSDDL); e != nil {
		return
	}
	dir, e := os.MkdirTemp(state, "session-")
	if e != nil {
		return
	}
	defer os.RemoveAll(dir)
	if e = securePath(dir, privateSDDL); e != nil {
		return
	}
	config, e := validateTUN(req.Config, filepath.Join(dir, "geoip-cn.srs"))
	if e != nil {
		_ = writeFrame(pipe, helperResponse{Error: e.Error()})
		return
	}
	if _, e = verifiedCore(filepath.Join(s.dir, "sing-box.exe")); e != nil {
		_ = writeFrame(pipe, helperResponse{Error: "已安装内核校验失败"})
		return
	}
	if e = os.WriteFile(filepath.Join(dir, "geoip-cn.srs"), req.Geo, 0600); e != nil {
		return
	}
	path := filepath.Join(dir, "config.json")
	if e = os.WriteFile(path, config, 0600); e != nil {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	check := exec.CommandContext(checkCtx, filepath.Join(s.dir, "sing-box.exe"), "check", "-c", path)
	HideCommand(check)
	e = check.Run()
	cancel()
	if e != nil {
		_ = writeFrame(pipe, helperResponse{Error: "TUN 内核配置校验失败"})
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, filepath.Join(s.dir, "sing-box.exe"), "run", "-c", path)
	HideCommand(cmd)
	cmd.Dir = dir
	if e = cmd.Start(); e != nil {
		_ = writeFrame(pipe, helperResponse{Error: "无法启动 TUN 内核"})
		return
	}
	job, e := AttachJob(cmd)
	if e != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return
	}
	defer job.Close()
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		cancel()
		job.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()
	select {
	case <-time.After(500 * time.Millisecond):
	case <-done:
		_ = writeFrame(pipe, helperResponse{Error: "TUN 内核启动失败"})
		return
	}
	if e = writeFrame(pipe, helperResponse{OK: true, Running: true}); e != nil {
		return
	}
	requestDone := make(chan error, 1)
	go func() {
		var stop helperRequest
		e := readFrame(pipe, &stop)
		if e == nil && stop.Command != "stop" {
			e = errors.New("invalid lease command")
		}
		requestDone <- e
	}()
	select {
	case <-ctx.Done():
	case <-done:
	case <-requestDone:
	}
	cancel()
	_ = job.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return
	}
	release()
	_ = writeFrame(pipe, helperResponse{OK: true, Running: false})
}
