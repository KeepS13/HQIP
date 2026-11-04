//go:build windows

package winclient

import (
	"errors"
	"golang.org/x/sys/windows"
	"net/url"
	"unsafe"
)

// OpenBrowser invokes the registered browser directly, without a shell parser.
func OpenBrowser(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "127.0.0.1")) {
		return errors.New("浏览器登录地址无效")
	}
	verb, _ := windows.UTF16PtrFromString("open")
	target, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return err
	}
	result, _, _ := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, 1)
	if result <= 32 {
		return errors.New("无法打开系统浏览器，请检查默认浏览器设置")
	}
	return nil
}
