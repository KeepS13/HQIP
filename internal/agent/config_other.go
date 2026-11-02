//go:build !linux

package agent

import "os"

func privateConfigMode(mode os.FileMode) error { return nil }
