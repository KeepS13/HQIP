//go:build windows

package winclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

const autoStartKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const autoStartValueName = "HQIP"

// The Run entry is the source of truth, independent of the signed-in HQIP
// account. Reading it never creates a key or restores an entry removed by the user.
func AutoStartEnabled() (bool, error) {
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	return (autoStartEntry{keyPath: autoStartKeyPath, executable: executable}).enabled()
}

// SetAutoStart only changes this application's entry for the current Windows
// user. It neither starts the client nor changes its VPN connection intent.
func SetAutoStart(enabled bool) error {
	entry := autoStartEntry{keyPath: autoStartKeyPath}
	if enabled {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		entry.executable = executable
	}
	return entry.setEnabled(enabled)
}

// keyPath allows registry integration tests to use an isolated non-startup key.
type autoStartEntry struct {
	keyPath    string
	executable string
}

func (entry autoStartEntry) command() (string, error) {
	if !filepath.IsAbs(entry.executable) || strings.ContainsAny(entry.executable, "\"\x00\r\n") {
		return "", errors.New("客户端程序路径无效")
	}
	// Always quote the executable, including paths with spaces or Chinese text.
	// Run keys have a documented 260-character command-line limit.
	command := `"` + entry.executable + `"`
	if len(utf16.Encode([]rune(command))) > 260 {
		return "", errors.New("客户端路径过长，请将客户端移至较短的固定目录后重新开启")
	}
	return command, nil
}

func (entry autoStartEntry) enabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, entry.keyPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	value, kind, err := key.GetStringValue(autoStartValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		// Keep a malformed existing entry removable through the switch.
		return errors.Is(err, registry.ErrUnexpectedType), err
	}
	if value == "" {
		return false, nil
	}
	command, err := entry.command()
	if err != nil {
		return true, err
	}
	if kind != registry.SZ || !strings.EqualFold(value, command) {
		return true, errors.New("启动项与当前客户端路径不一致，请关闭后重新开启开机自启")
	}
	return true, nil
}

func (entry autoStartEntry) setEnabled(enabled bool) error {
	if !enabled {
		key, err := registry.OpenKey(registry.CURRENT_USER, entry.keyPath, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		err = key.DeleteValue(autoStartValueName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	command, err := entry.command()
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, entry.keyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(autoStartValueName, command)
}
