package main

import (
	"fmt"

	"github.com/KeepS13/HQIP/internal/winclient"
)

type AutoStartState struct {
	Enabled bool   `json:"enabled"`
	Error   string `json:"error"`
}

func readAutoStartState() AutoStartState {
	enabled, err := winclient.AutoStartEnabled()
	state := AutoStartState{Enabled: enabled}
	if err != nil {
		state.Error = fmt.Sprintf("读取开机自启设置失败：%v", err)
	}
	return state
}

func (d *Desktop) SetAutoStart(enabled bool) error {
	if err := winclient.SetAutoStart(enabled); err != nil {
		return fmt.Errorf("保存开机自启设置失败：%w", err)
	}
	return nil
}
