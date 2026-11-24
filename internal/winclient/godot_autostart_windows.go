//go:build windows

package winclient

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// GodotAutoStart targets the stable launcher, never the versioned companion.
// A separate value preserves the original Wails startup preference during trial.
// nil is read-only; non-nil changes only this current user's Godot entry.
func GodotAutoStart(launcher string, enabled *bool) (bool, error) {
	return godotAutoStart(autoStartKeyPath, launcher, enabled)
}

func godotAutoStart(keyPath, launcher string, enabled *bool) (bool, error) {
	const valueName = "HQIP Godot"
	command, err := (autoStartEntry{executable: launcher}).command()
	if err != nil {
		return false, err
	}
	if enabled != nil && *enabled {
		key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
		if err != nil {
			return false, err
		}
		defer key.Close()
		return true, key.SetStringValue(valueName, command)
	}
	access := uint32(registry.QUERY_VALUE)
	if enabled != nil {
		access = registry.SET_VALUE
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, keyPath, access)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	if enabled != nil {
		err = key.DeleteValue(valueName)
		if errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
		return false, err
	}
	value, kind, err := key.GetStringValue(valueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if kind != registry.SZ || !strings.EqualFold(value, command) {
		return true, errors.New("startup path changed")
	}
	return true, nil
}
