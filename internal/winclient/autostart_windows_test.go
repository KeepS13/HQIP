//go:build windows

package winclient

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func isolatedAutoStart(t *testing.T) autoStartEntry {
	t.Helper()
	path := fmt.Sprintf(`Software\HQIP-AutoStart-Test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })
	return autoStartEntry{keyPath: path, executable: `C:\应用 程序\HQIP & VPN\HQIP.exe`}
}

func TestAutoStartRegistryRoundTrip(t *testing.T) {
	entry := isolatedAutoStart(t)
	if enabled, err := entry.enabled(); enabled || err != nil {
		t.Fatalf("new installation: %v %v", enabled, err)
	}
	if err := entry.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if key, err := registry.OpenKey(registry.CURRENT_USER, entry.keyPath, registry.QUERY_VALUE); err == nil {
		key.Close()
		t.Fatal("reading/disabling created a registry key")
	}
	for range 2 {
		if err := entry.setEnabled(true); err != nil {
			t.Fatal(err)
		}
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, entry.keyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	command, kind, err := key.GetStringValue(autoStartValueName)
	if err != nil || kind != registry.SZ {
		t.Fatalf("Run value: %d %v", kind, err)
	}
	args, err := windows.DecomposeCommandLine(command)
	if err != nil || len(args) != 1 || args[0] != entry.executable {
		t.Fatalf("executable path did not round-trip through Windows command parsing: %q %v", args, err)
	}
	// A fresh manager (like a restarted client) reads the persisted OS setting.
	copy := autoStartEntry{keyPath: entry.keyPath, executable: entry.executable}
	if enabled, err := copy.enabled(); !enabled || err != nil {
		t.Fatalf("persisted setting: %v %v", enabled, err)
	}
	if err := key.SetStringValue("OtherApplication", "leave this alone"); err != nil {
		t.Fatal(err)
	}
	entry.executable = "" // Disabling must not depend on locating the executable.
	for range 2 {
		if err := entry.setEnabled(false); err != nil {
			t.Fatal(err)
		}
	}
	if enabled, err := copy.enabled(); enabled || err != nil {
		t.Fatalf("disabled: %v %v", enabled, err)
	}
	if value, _, err := key.GetStringValue("OtherApplication"); value != "leave this alone" || err != nil {
		t.Fatalf("unrelated startup entry modified: %q %v", value, err)
	}
}

func TestAutoStartMovedAndMalformedEntriesRemainRemovable(t *testing.T) {
	entry := isolatedAutoStart(t)
	if err := entry.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	entry.executable = `D:\New HQIP\HQIP.exe`
	if enabled, err := entry.enabled(); !enabled || err == nil {
		t.Fatalf("stale path must be reported and removable: %v %v", enabled, err)
	}
	if err := entry.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if enabled, err := entry.enabled(); !enabled || err != nil {
		t.Fatalf("new executable not registered: %v %v", enabled, err)
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, entry.keyPath, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	if err := key.SetDWordValue(autoStartValueName, 1); err != nil {
		t.Fatal(err)
	}
	if enabled, err := entry.enabled(); !enabled || err == nil {
		t.Fatalf("malformed entry must be reported and removable: %v %v", enabled, err)
	}
	if err := entry.setEnabled(false); err != nil {
		t.Fatal(err)
	}
}

func TestAutoStartRejectsInvalidCommandsWithoutChangingEntry(t *testing.T) {
	entry := isolatedAutoStart(t)
	if err := entry.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", `relative\HQIP.exe`, "C:\\bad\x00.exe", `C:\bad"name.exe`, `C:\` + strings.Repeat("长", 260) + `.exe`} {
		invalid := autoStartEntry{keyPath: entry.keyPath, executable: path}
		if err := invalid.setEnabled(true); err == nil {
			t.Fatalf("accepted invalid executable: %q", path)
		}
		if enabled, err := entry.enabled(); !enabled || err != nil {
			t.Fatalf("failed write changed previous setting: %v %v", enabled, err)
		}
	}
	invalidKey := autoStartEntry{keyPath: "Software\x00Invalid", executable: entry.executable}
	if _, err := invalidKey.enabled(); err == nil {
		t.Fatal("registry read failure was hidden")
	}
	for _, enabled := range []bool{true, false} {
		if err := invalidKey.setEnabled(enabled); err == nil {
			t.Fatal("registry write failure was hidden")
		}
	}
}
