//go:build windows

package winclient

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestDPAPIRoundTripAndTamperDetection(t *testing.T) {
	plain := []byte("private client token and cache")
	cipher, e := Protect(plain)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(cipher, plain) {
		t.Fatal("plaintext retained")
	}
	decoded, e := Unprotect(cipher)
	if e != nil || !bytes.Equal(decoded, plain) {
		t.Fatal("DPAPI round trip failed", e)
	}
	cipher[len(cipher)-1] ^= 1
	if _, e = Unprotect(cipher); e == nil {
		t.Fatal("tampered DPAPI payload accepted")
	}
}

func TestJobCloseTerminatesHiddenChild(t *testing.T) {
	if os.Getenv("HQIP_JOB_TEST_CHILD") == "1" {
		time.Sleep(time.Minute)
		return
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestJobCloseTerminatesHiddenChild$")
	cmd.Env = append(os.Environ(), "HQIP_JOB_TEST_CHILD=1")
	HideCommand(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("child window not hidden")
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer cmd.Process.Kill()
	job, e := AttachJob(cmd)
	if e != nil {
		cmd.Wait()
		t.Fatal(e)
	}
	if e = job.Close(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job close left core running")
	}
}

func TestSecureDirectoryAndEncryptedProxyLease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if e := SecureDir(dir); e != nil {
		t.Fatal(e)
	}
	p := NewProxyManager(dir)
	lease := proxyLease{PID: 123, Stamp: 456, Before: map[string]proxyValue{"ProxyServer": {Present: true, Text: "private proxy address"}}, Owned: map[string]proxyValue{}}
	if e := p.save(lease); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(dir, "proxy.lease"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("private proxy address")) {
		t.Fatal("lease saved plaintext")
	}
	got, e := p.load()
	if e != nil || got.PID != 123 || got.Before["ProxyServer"].Text != "private proxy address" {
		t.Fatal("lease recovery failed", e)
	}
}
