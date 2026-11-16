package main

import (
	"testing"

	"github.com/KeepS13/HQIP/internal/client"
)

func TestConnectionIconsReflectActualVPNState(t *testing.T) {
	for _, test := range []struct {
		name, runState, want               string
		loggedIn, intent, selectionPending bool
	}{
		{"logged-out", "connected", "disconnected", false, true, false},
		{"idle", "disconnected", "disconnected", true, false, false},
		{"starting", "connecting", "connecting", true, true, false},
		{"online", "connected", "connected", true, true, false},
		{"recovering", "recovering", "recovering", true, true, false},
		{"failure", "failed", "failed", true, false, false},
		{"online-switch", "connected", "connecting", true, true, true},
		{"offline-switch", "disconnected", "disconnected", true, false, true},
		{"unknown", "unexpected", "disconnected", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := client.View{LoggedIn: test.loggedIn, SelectionPending: test.selectionPending,
				Status: client.Status{RunState: test.runState, ConnectionIntent: test.intent}}
			got := connectionIcon(view)
			if got.Name != test.want || got.Label == "" {
				t.Fatalf("wrong status icon: %+v, want %s", got, test.want)
			}
			if test.runState == "recovering" && got.Label != "恢复中" {
				t.Fatalf("background recovery must keep the connected tooltip: %+v", got)
			}
			if _, err := statusIconAssets.ReadFile("assets/hqip-" + got.Name + ".ico"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
