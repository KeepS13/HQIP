package client

import (
	"context"
	"testing"
)

// Signed sequence orders releases independently of the human-facing version.
// Resetting the product name must not disable updates or permit a replay.
func TestBundleUpdateUsesSequenceAcrossVersionReset(t *testing.T) {
	for _, tt := range []struct {
		name      string
		version   string
		sequence  int64
		wantReady bool
	}{
		{"reset-version", "1.0.0", 5000019, true},
		{"same-version-new-build", "5.0.0", 5000019, true},
		{"same-sequence", "6.0.0", 5000017, false},
		{"replayed-sequence", "6.0.0", 5000016, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newBundleFixture(t)
			f.publish(tt.version, tt.sequence)
			u := f.updater()
			u.installed = 5000017
			if err := u.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if u.View().Ready != tt.wantReady {
				t.Fatalf("ready=%v, want %v", u.View().Ready, tt.wantReady)
			}
			if tt.wantReady && (f.downloads != 1 || f.stages != 1 || u.View().Version != tt.version) {
				t.Fatalf("update not staged correctly: view=%+v downloads=%d stages=%d", u.View(), f.downloads, f.stages)
			}
			if !tt.wantReady && (f.downloads != 0 || f.stages != 0) {
				t.Fatal("older sequence reached download or installer")
			}
		})
	}
}
