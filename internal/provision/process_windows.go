//go:build windows

package provision

import (
	"os/exec"
	"syscall"
)

func hideProbeCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
