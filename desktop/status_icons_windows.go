//go:build windows

package main

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

type windowStatusIcons struct {
	handle                       func() w32.HWND
	small, large                 w32.HICON
	originalSmall, originalLarge w32.HICON
	attached                     bool
}

func newWindowStatusIcons(window *application.WebviewWindow) *windowStatusIcons {
	return &windowStatusIcons{handle: func() w32.HWND { return w32.HWND(uintptr(window.NativeWindow())) }}
}

func (icons *windowStatusIcons) set(data []byte) error {
	hwnd := icons.handle()
	if hwnd == 0 || !w32.IsWindow(hwnd) {
		return errors.New("客户端窗口尚未就绪")
	}
	small, err := w32.CreateSmallHIconFromImage(data)
	if err != nil {
		return err
	}
	large, err := w32.CreateLargeHIconFromImage(data)
	if err != nil {
		w32.DestroyIcon(small)
		return err
	}
	previousSmall := w32.HICON(w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_SMALL, uintptr(small)))
	previousLarge := w32.HICON(w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_BIG, uintptr(large)))
	if !icons.attached {
		// These initial handles belong to Wails/PE resources. Never destroy them.
		icons.originalSmall, icons.originalLarge = previousSmall, previousLarge
		icons.attached = true
	}
	icons.release()
	icons.small, icons.large = small, large
	return nil
}

func (icons *windowStatusIcons) release() {
	if icons.small != 0 {
		w32.DestroyIcon(icons.small)
		icons.small = 0
	}
	if icons.large != 0 {
		w32.DestroyIcon(icons.large)
		icons.large = 0
	}
}

func (icons *windowStatusIcons) close() {
	if icons.attached {
		hwnd := icons.handle()
		if hwnd != 0 && w32.IsWindow(hwnd) {
			w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_SMALL, uintptr(icons.originalSmall))
			w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_BIG, uintptr(icons.originalLarge))
		}
	}
	icons.release()
	icons.attached = false
}
