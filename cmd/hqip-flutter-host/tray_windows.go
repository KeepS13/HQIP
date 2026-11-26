package main

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var user32 = windows.NewLazySystemDLL("user32.dll")
var shell32 = windows.NewLazySystemDLL("shell32.dll")
var trayWindow atomic.Uintptr

type point struct{ X, Y int32 }
type message struct {
	Window         uintptr
	ID             uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}
type windowClass struct {
	Size, Style                        uint32
	Procedure                          uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type notifyIcon struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	Title               [64]uint16
	InfoFlags           uint32
	GUID                windows.GUID
	Balloon             uintptr
}

func utf(value string) *uint16 { p, _ := windows.UTF16PtrFromString(value); return p }
func postTrayQuit() {
	if hwnd := trayWindow.Load(); hwnd != 0 {
		user32.NewProc("PostMessageW").Call(hwnd, 0x10, 0, 0)
	}
}
func runTray(ctx context.Context, iconPath string, open, disconnect, quit func(), state func() string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instance, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	var data notifyIcon
	taskbar, _, _ := user32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(utf("TaskbarCreated"))))
	procedure := syscall.NewCallback(func(hwnd uintptr, msg uint32, w, l uintptr) uintptr {
		switch msg {
		case 0x8001:
			if uint32(l) == 0x202 || uint32(l) == 0x203 {
				open()
			}
			if uint32(l) == 0x205 {
				menu, _, _ := user32.NewProc("CreatePopupMenu").Call()
				defer user32.NewProc("DestroyMenu").Call(menu)
				for _, item := range []struct {
					id   uintptr
					text string
				}{{1, "打开 HQIP Preview"}, {2, "断开连接"}, {3, "退出并恢复网络"}} {
					user32.NewProc("AppendMenuW").Call(menu, 0, item.id, uintptr(unsafe.Pointer(utf(item.text))))
				}
				var p point
				user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&p)))
				user32.NewProc("SetForegroundWindow").Call(hwnd)
				choice, _, _ := user32.NewProc("TrackPopupMenu").Call(menu, 0x100|2, uintptr(p.X), uintptr(p.Y), 0, hwnd, 0)
				switch choice {
				case 1:
					open()
				case 2:
					disconnect()
				case 3:
					quit()
				}
			}
			return 0
		case 0x113:
			label := map[string]string{"connected": "已连接", "disconnected": "未连接", "connecting": "连接中", "recovering": "恢复中", "failed": "连接失败"}[state()]
			tip, _ := windows.UTF16FromString("HQIP Preview · " + label)
			clear(data.Tip[:])
			copy(data.Tip[:], tip)
			shell32.NewProc("Shell_NotifyIconW").Call(1, uintptr(unsafe.Pointer(&data)))
			return 0
		case 0x10:
			user32.NewProc("DestroyWindow").Call(hwnd)
			return 0
		case 2:
			shell32.NewProc("Shell_NotifyIconW").Call(2, uintptr(unsafe.Pointer(&data)))
			user32.NewProc("PostQuitMessage").Call(0)
			return 0
		}
		if msg == uint32(taskbar) {
			shell32.NewProc("Shell_NotifyIconW").Call(0, uintptr(unsafe.Pointer(&data)))
			return 0
		}
		result, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), w, l)
		return result
	})
	class := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), Procedure: procedure, Instance: instance, Name: utf("HQIPFlutterPreviewTray")}
	if result, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class))); result == 0 {
		return fmt.Errorf("register tray: %v", err)
	}
	hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class.Name)), uintptr(unsafe.Pointer(utf("HQIP Preview"))), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("create tray: %v", err)
	}
	trayWindow.Store(hwnd)
	defer trayWindow.Store(0)
	go func() { <-ctx.Done(); postTrayQuit() }()
	icon, _, _ := user32.NewProc("LoadImageW").Call(0, uintptr(unsafe.Pointer(utf(iconPath))), 1, 32, 32, 0x10)
	if icon == 0 {
		icon, _, _ = user32.NewProc("LoadIconW").Call(0, 32512)
	} else {
		defer user32.NewProc("DestroyIcon").Call(icon)
	}
	data = notifyIcon{Size: uint32(unsafe.Sizeof(notifyIcon{})), Window: hwnd, ID: 1, Flags: 1 | 2 | 4, Callback: 0x8001, Icon: icon}
	copy(data.Tip[:], []uint16{'H', 'Q', 'I', 'P', ' ', 'P', 'r', 'e', 'v', 'i', 'e', 'w', 0})
	if ok, _, err := shell32.NewProc("Shell_NotifyIconW").Call(0, uintptr(unsafe.Pointer(&data))); ok == 0 {
		user32.NewProc("DestroyWindow").Call(hwnd)
		return fmt.Errorf("create tray icon: %v", err)
	}
	user32.NewProc("SetTimer").Call(hwnd, 1, 1000, 0)
	var msg message
	for {
		value, _, _ := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(value) <= 0 {
			break
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

var focusCallback = syscall.NewCallback(func(hwnd, extra uintptr) uintptr {
	var owner uint32
	user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&owner)))
	if owner == uint32(extra) {
		visible, _, _ := user32.NewProc("IsWindowVisible").Call(hwnd)
		if visible != 0 {
			user32.NewProc("ShowWindow").Call(hwnd, 9)
			user32.NewProc("SetForegroundWindow").Call(hwnd)
			return 0
		}
	}
	return 1
})

func focusProcess(pid uint32) { user32.NewProc("EnumWindows").Call(focusCallback, uintptr(pid)) }
