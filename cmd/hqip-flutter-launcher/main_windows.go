//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/KeepS13/HQIP/internal/clientrelease"
	"github.com/KeepS13/HQIP/internal/flutterrelease"
	"github.com/KeepS13/HQIP/internal/previewhost"
	"golang.org/x/sys/windows"
)

var updateKey string
var bundledSequence = "1"
var startupTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if conn, err := previewhost.Dial(ctx); err == nil {
		defer conn.Close()
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		_, err = conn.Write([]byte("{\"id\":1,\"method\":\"openWindow\"}\n"))
		return err
	}
	name, _ := windows.UTF16PtrFromString("Local\\HQIP.Flutter.Preview")
	mutex, err := windows.CreateMutex(nil, false, name)
	if mutex != 0 {
		defer windows.CloseHandle(mutex)
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil
	}
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	root := filepath.Dir(exe)
	for {
		restart, err := launch(root)
		if err != nil {
			return err
		}
		if !restart {
			return nil
		}
	}
}
func verified(root string, raw []byte) (string, error) {
	var e flutterrelease.Envelope
	if json.Unmarshal(raw, &e) != nil {
		return "", errors.New("invalid version pointer")
	}
	r, err := e.Verify(updateKey)
	if err != nil {
		return "", err
	}
	minimum, err := strconv.ParseInt(bundledSequence, 10, 64)
	if err != nil || r.Sequence < minimum || r.Platform != "windows-amd64" || r.Kind != "update" {
		return "", errors.New("invalid version sequence")
	}
	if err = flutterrelease.VerifyTree(filepath.Join(root, "versions", r.SHA256), r); err != nil {
		return "", err
	}
	return r.SHA256, nil
}
func launch(root string) (bool, error) {
	active := "bundled"
	var previous []byte
	if raw, err := os.ReadFile(filepath.Join(root, "active.json")); err == nil {
		if value, err := verified(root, raw); err == nil {
			active = value
			previous = raw
		}
	}
	if pending, err := os.ReadFile(filepath.Join(root, "pending.json")); err == nil {
		if next, err := verified(root, pending); err == nil && next != active {
			restart, err := start(root, next, pending)
			if err == nil {
				return restart, nil
			}
			if len(previous) > 0 {
				_ = clientrelease.WriteAtomic(filepath.Join(root, "active.json"), previous)
			} else {
				_ = os.Remove(filepath.Join(root, "active.json"))
			}
		}
		_ = os.Remove(filepath.Join(root, "pending.json"))
	}
	restart, err := start(root, active, nil)
	if err != nil && active != "bundled" {
		_ = os.Remove(filepath.Join(root, "active.json"))
		return start(root, "bundled", nil)
	}
	return restart, err
}
func start(root, version string, pending []byte) (bool, error) {
	dir := filepath.Join(root, "versions", version)
	marker := filepath.Join(dir, ".startup-ready")
	_ = os.Remove(marker)
	args := []string{"--startup-marker", marker}
	for _, arg := range os.Args[1:] {
		if arg == "--background" {
			args = append(args, arg)
		}
	}
	command := exec.Command(filepath.Join(dir, "hqip-runtime.exe"), args...)
	command.Dir = dir
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := command.Start(); err != nil {
		return false, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(startupTimeout)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("runtime exited before ready")
			}
			return false, err
		case <-timer.C:
			stopChild(command, done)
			return false, errors.New("preview startup timed out")
		case <-tick.C:
			if _, err := os.Stat(marker); err == nil {
				if pending != nil {
					if err = clientrelease.WriteAtomic(filepath.Join(root, "active.json"), pending); err != nil {
						stopChild(command, done)
						return false, err
					}
				}
				_ = os.Remove(filepath.Join(root, "pending.json"))
				_ = os.Remove(marker)
				err = <-done
				var exit *exec.ExitError
				if errors.As(err, &exit) && exit.ExitCode() == 42 {
					return true, nil
				}
				return false, err
			}
		}
	}
}

// Ask the owned host to restore networking before terminating a broken startup.
// The bounded fallback also ends its UI descendants, preventing orphan windows.
func stopChild(command *exec.Cmd, done <-chan error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if conn, err := previewhost.Dial(ctx); err == nil {
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = conn.Write([]byte("{\"id\":1,\"method\":\"quitApp\"}\n"))
		_ = conn.Close()
	}
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
	}
	kill := exec.Command("taskkill.exe", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = kill.Run()
	_ = command.Process.Kill()
	<-done
}
