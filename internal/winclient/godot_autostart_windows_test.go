//go:build windows

package winclient

import (
	"golang.org/x/sys/windows/registry"
	"testing"
)

func TestGodotStartupUsesStableLauncherAndPreservesWails(t *testing.T) {
	entry := isolatedAutoStart(t)
	if enabled, err := godotAutoStart(entry.keyPath, entry.executable, nil); enabled || err != nil {
		t.Fatalf("initial: %v %v", enabled, err)
	}
	// This is a random test registry key, never the user's real startup key.
	if err := entry.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	on := true
	if enabled, err := godotAutoStart(entry.keyPath, entry.executable, &on); !enabled || err != nil {
		t.Fatalf("enable: %v %v", enabled, err)
	}
	if enabled, err := godotAutoStart(entry.keyPath, entry.executable, nil); !enabled || err != nil {
		t.Fatalf("read: %v %v", enabled, err)
	}
	off := false
	if _, err := godotAutoStart(entry.keyPath, entry.executable, &off); err != nil {
		t.Fatal(err)
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, entry.keyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	if _, _, err = key.GetStringValue("HQIP"); err != nil {
		t.Fatal("original startup value was touched")
	}
}
