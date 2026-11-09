//go:build windows

package winclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHelperUpgradeOnActiveConnectAndCancellation(t *testing.T) {
	for _, kind := range []string{"current", "old", "denied", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "pipe")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			opens, installs := 0, 0
			pipe, err := connectVersionedHelper(ctx, func() (*os.File, error) {
				opens++
				if kind == "current" || opens > 1 {
					return file, nil
				}
				return nil, errors.New("outdated helper")
			}, func() error {
				installs++
				if kind == "denied" {
					return errors.New("UAC declined")
				}
				return nil
			})
			switch kind {
			case "current":
				if pipe != file || err != nil || installs != 0 {
					t.Fatal("upgraded matching helper")
				}
			case "old":
				if pipe != file || err != nil || installs != 1 || opens != 2 {
					t.Fatal("did not upgrade and revalidate old helper")
				}
			case "denied":
				if err == nil || pipe != nil || installs != 1 {
					t.Fatal("ignored upgrade failure")
				}
			case "canceled":
				if err == nil || opens != 0 || installs != 0 {
					t.Fatal("canceled connect installed helper")
				}
			}
		})
	}
}

func TestHelperUpgradeRequiresMatchingBundledBinary(t *testing.T) {
	dir := t.TempDir()
	bundled, installed := filepath.Join(dir, "bundled.exe"), filepath.Join(dir, "installed.exe")
	os.WriteFile(bundled, []byte("new-validator"), 0600)
	if matchingHelperBinary(bundled, installed) == nil {
		t.Fatal("missing helper accepted")
	}
	os.WriteFile(installed, []byte("old-validator"), 0600)
	if matchingHelperBinary(bundled, installed) == nil {
		t.Fatal("outdated helper accepted")
	}
	os.WriteFile(installed, []byte("new-validator"), 0600)
	if err := matchingHelperBinary(bundled, installed); err != nil {
		t.Fatal(err)
	}
}
