package winclient

import (
	"errors"
	"golang.org/x/sys/windows/registry"
	"strings"
)

// PreviewAutoStart deliberately owns a different Run value from both old apps.
func PreviewAutoStart(launcher string, enabled *bool) (bool, error) {
	const name = "HQIP Preview"
	command, err := (autoStartEntry{executable: launcher}).command()
	if err != nil {
		return false, err
	}
	command += " --background"
	access := uint32(registry.QUERY_VALUE)
	if enabled != nil {
		access |= registry.SET_VALUE
	}
	var key registry.Key
	if enabled != nil && *enabled {
		key, _, err = registry.CreateKey(registry.CURRENT_USER, autoStartKeyPath, access)
	} else {
		key, err = registry.OpenKey(registry.CURRENT_USER, autoStartKeyPath, access)
	}
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	if enabled != nil {
		if *enabled {
			return true, key.SetStringValue(name, command)
		}
		err = key.DeleteValue(name)
		if errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
		return false, err
	}
	value, kind, err := key.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if kind != registry.SZ || !strings.EqualFold(value, command) {
		return true, errors.New("启动路径已改变，请重新开启")
	}
	return true, nil
}
