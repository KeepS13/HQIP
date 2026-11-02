//go:build !windows

package winclient

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

var errUnsupported = errors.New("HQIP 桌面网络功能仅支持 Windows")

type Adapter struct{}

func New(string, string) (*Adapter, error)                              { return nil, errUnsupported }
func (*Adapter) SecureDir(string) error                                 { return errUnsupported }
func (*Adapter) Protect([]byte) ([]byte, error)                         { return nil, errUnsupported }
func (*Adapter) Unprotect([]byte) ([]byte, error)                       { return nil, errUnsupported }
func (*Adapter) Prepare(*exec.Cmd)                                      {}
func (*Adapter) Attach(*exec.Cmd) (io.Closer, error)                    { return nil, errUnsupported }
func (*Adapter) EnableProxy(int) error                                  { return errUnsupported }
func (*Adapter) RestoreProxy() error                                    { return nil }
func (*Adapter) StartTUN(context.Context, []byte) (func() error, error) { return nil, errUnsupported }
func HelperMain([]string) error                                         { return errUnsupported }
