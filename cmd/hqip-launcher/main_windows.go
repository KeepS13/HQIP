//go:build windows

// The small stable launcher activates already-verified complete versions. It
// never downloads code and never overwrites a running executable.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/KeepS13/HQIP/internal/clientrelease"
	"golang.org/x/sys/windows"
)

var updateKey string
var startupTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	root := filepath.Dir(executable)
	// All installation folders share the same account store for this user.
	name, _ := windows.UTF16PtrFromString("Local\\HQIP-Godot")
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
	return launch(root)
}

// launch preserves a known-good pointer before activation. Even a candidate
// that crashes after its readiness marker is rolled back on disk and in memory.
func launch(root string) error {
	active := "bundled"
	var activeRaw []byte
	if raw, err := os.ReadFile(filepath.Join(root, "active.json")); err == nil {
		if value, err := verifiedVersion(raw); err == nil {
			active = value
			activeRaw = raw
		}
	}
	if pending, err := os.ReadFile(filepath.Join(root, "pending.json")); err == nil {
		if candidate, verifyErr := verifiedVersion(pending); verifyErr == nil && candidate != active {
			// Only a candidate that renders and obtains its first native state can
			// become active. A failed start is stopped before launching the fallback.
			if startErr := start(root, candidate, pending); startErr == nil {
				return nil
			}
			if len(activeRaw) == 0 {
				_ = os.Remove(filepath.Join(root, "active.json"))
			} else if err := clientrelease.WriteAtomic(filepath.Join(root, "active.json"), activeRaw); err != nil {
				return err
			}
		}
		_ = os.Remove(filepath.Join(root, "pending.json"))
	}
	if err := start(root, active, nil); err != nil {
		if active == "bundled" {
			return err
		}
		_ = os.Remove(filepath.Join(root, "active.json"))
		return start(root, "bundled", nil)
	}
	return nil
}
func verifiedVersion(raw []byte) (string, error) {
	var envelope clientrelease.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", err
	}
	release, err := envelope.Verify(updateKey)
	if err != nil {
		return "", err
	}
	if release.Platform != "windows-amd64" {
		return "", fmt.Errorf("wrong update platform")
	}
	return release.SHA256, nil
}
func start(root, version string, pending []byte) error {
	dir := filepath.Join(root, "versions", version)
	marker := filepath.Join(dir, ".startup-ready")
	_ = os.Remove(marker)
	command := exec.Command(filepath.Join(dir, "HQIP-UI.exe"), "--", "--startup-marker="+marker)
	command.Dir = dir
	if err := command.Start(); err != nil {
		return err
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
				err = fmt.Errorf("candidate exited before readiness")
			}
			return err
		case <-timer.C:
			_ = command.Process.Kill()
			<-done
			return fmt.Errorf("candidate startup timeout")
		case <-tick.C:
			if _, err := os.Stat(marker); err == nil {
				if pending != nil {
					err = clientrelease.WriteAtomic(filepath.Join(root, "active.json"), pending)
				}
				if err != nil {
					_ = command.Process.Kill()
					<-done
					return err
				}
				_ = os.Remove(filepath.Join(root, "pending.json"))
				_ = os.Remove(marker)
				return <-done
			}
		}
	}
}
