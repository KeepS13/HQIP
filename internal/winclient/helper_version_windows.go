//go:build windows

package winclient

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func connectVersionedHelper(ctx context.Context, open func() (*os.File, error), install func() error) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pipe, err := open(); err == nil {
		return pipe, nil
	}
	if err := install(); err != nil {
		return nil, err
	}
	deadline, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		if err := deadline.Err(); err != nil {
			return nil, err
		}
		if pipe, err := open(); err == nil {
			return pipe, nil
		}
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-deadline.Done():
			timer.Stop()
			return nil, errors.New("等待 TUN 服务安装或升级超时或已取消")
		case <-timer.C:
		}
	}
}

// Never send a new configuration to an older installed validator. Upgrade is
// requested only by startTUN, which runs in the explicit connection path.
func matchingHelperPipe(sid, bundled string) (*os.File, error) {
	pipe, err := pipeClient(sid)
	if err != nil {
		return nil, err
	}
	dir, err := installedDir()
	if err == nil {
		err = matchingHelperBinary(bundled, filepath.Join(dir, "hqip-helper.exe"))
	}
	if err != nil {
		pipe.Close()
		return nil, err
	}
	return pipe, nil
}

func matchingHelperBinary(bundled, installed string) error {
	want, err := os.ReadFile(bundled)
	if err != nil {
		return err
	}
	actual, err := os.ReadFile(installed)
	if err != nil {
		return err
	}
	if len(want) == 0 || sha256.Sum256(want) != sha256.Sum256(actual) {
		return errors.New("TUN Helper 需要升级")
	}
	return nil
}
