//go:build !windows

package winclient

func AutoStartEnabled() (bool, error) { return false, errUnsupported }
func SetAutoStart(bool) error         { return errUnsupported }
