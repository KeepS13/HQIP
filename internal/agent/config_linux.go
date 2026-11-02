//go:build linux

package agent

import (
	"errors"
	"os"
)

func privateConfigMode(mode os.FileMode) error {
	if mode.Perm()&0077 != 0 {
		return errors.New("Agent 配置含密钥，权限必须为 0600")
	}
	return nil
}
