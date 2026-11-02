//go:build windows

package winclient

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
)

type Adapter struct {
	stateDir, corePath string
	proxy              *ProxyManager
}

func New(stateDir, corePath string) (*Adapter, error) {
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	corePath, err = filepath.Abs(corePath)
	if err != nil {
		return nil, err
	}
	if err = SecureDir(stateDir); err != nil {
		return nil, err
	}
	a := &Adapter{stateDir: stateDir, corePath: corePath, proxy: NewProxyManager(stateDir)}
	a.proxy.helperPath = filepath.Join(filepath.Dir(corePath), "hqip-helper.exe")
	if err = a.proxy.Recover(); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *Adapter) SecureDir(p string) error              { return SecureDir(p) }
func (a *Adapter) Protect(p []byte) ([]byte, error)      { return Protect(p) }
func (a *Adapter) Unprotect(p []byte) ([]byte, error)    { return Unprotect(p) }
func (a *Adapter) Prepare(c *exec.Cmd)                   { HideCommand(c) }
func (a *Adapter) Attach(c *exec.Cmd) (io.Closer, error) { return AttachJob(c) }
func (a *Adapter) EnableProxy(p int) error               { return a.proxy.Enable(p) }
func (a *Adapter) RestoreProxy() error                   { return a.proxy.Restore() }
func (a *Adapter) StartTUN(ctx context.Context, b []byte) (func() error, error) {
	return a.startTUN(ctx, b)
}
