//go:build windows

package main

import (
	"runtime"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

func TestStatusIconsUpdateNativeWindowAndReleaseHandles(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, _ := windows.UTF16PtrFromString("STATIC")
	title, _ := windows.UTF16PtrFromString("HQIP icon test")
	// A hidden test window exercises WM_SETICON without opening the VPN client.
	hwnd := w32.CreateWindowEx(0, class, title, w32.WS_OVERLAPPEDWINDOW, 0, 0, 64, 64, 0, 0, w32.GetModuleHandle(""), nil)
	if hwnd == 0 {
		t.Fatal("could not create native test window")
	}
	defer w32.DestroyWindow(hwnd)
	icons := &windowStatusIcons{handle: func() w32.HWND { return hwnd }}
	defer icons.close()
	var previous uintptr
	for _, name := range []string{"disconnected", "connecting", "connected", "recovering", "failed", "disconnected"} {
		data, err := statusIconAssets.ReadFile("assets/hqip-" + name + ".ico")
		if err != nil {
			t.Fatal(err)
		}
		if err := icons.set(data); err != nil {
			t.Fatal(err)
		}
		large := w32.SendMessage(hwnd, w32.WM_GETICON, w32.ICON_BIG, 0)
		if large == 0 || large != uintptr(icons.large) || large == previous {
			t.Fatalf("native large icon did not change for %s", name)
		}
		if small := w32.SendMessage(hwnd, w32.WM_GETICON, w32.ICON_SMALL, 0); small == 0 || small != uintptr(icons.small) {
			t.Fatalf("native small icon missing for %s", name)
		}
		previous = large
	}
	if err := icons.set([]byte("invalid icon")); err == nil {
		t.Fatal("invalid icon accepted")
	}
	if w32.SendMessage(hwnd, w32.WM_GETICON, w32.ICON_BIG, 0) != previous {
		t.Fatal("failed icon load replaced working icon")
	}
	icons.close()
	if icons.small != 0 || icons.large != 0 || icons.attached {
		t.Fatal("native handles retained after shutdown")
	}
	if w32.SendMessage(hwnd, w32.WM_GETICON, w32.ICON_BIG, 0) != uintptr(icons.originalLarge) {
		t.Fatal("original window icon not restored")
	}
}
