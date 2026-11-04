//go:build !windows

package winclient

import "errors"

func OpenBrowser(string) error { return errors.New("浏览器登录仅支持 Windows 客户端") }
