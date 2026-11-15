//go:build !windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

type windowStatusIcons struct{}

func newWindowStatusIcons(*application.WebviewWindow) *windowStatusIcons { return &windowStatusIcons{} }
func (*windowStatusIcons) set([]byte) error                              { return nil }
func (*windowStatusIcons) close()                                        {}
