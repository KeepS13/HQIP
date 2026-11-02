//go:build !windows

package provision

import "os/exec"

func hideProbeCommand(cmd *exec.Cmd) {}
