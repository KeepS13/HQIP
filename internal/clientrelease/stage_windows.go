//go:build windows

package clientrelease

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StageWindows publishes a pending pointer only after complete extraction. The
// launcher owns activation; running binaries and active connections stay intact.
func StageWindows(root, archive string, release Release, envelope Envelope) error {
	if err := release.Validate(); err != nil {
		return err
	}
	versions := filepath.Join(root, "versions")
	if err := os.MkdirAll(versions, 0700); err != nil {
		return err
	}
	destination := filepath.Join(versions, release.SHA256)
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	marker := filepath.Join(destination, "release.json")
	if existing, readErr := os.ReadFile(marker); readErr != nil || string(existing) != string(raw) {
		// A crash while extracting cannot strand an incomplete final version.
		// Only our unique temporary directory is cleaned; existing versions stay.
		temporary, createErr := os.MkdirTemp(versions, ".prepare-")
		if createErr != nil {
			return createErr
		}
		defer os.RemoveAll(temporary)
		payload := filepath.Join(temporary, "payload")
		if err = ExtractWindows(archive, payload, release); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(payload, "release.json"), raw, 0600); err != nil {
			return err
		}
		if err = os.Rename(payload, destination); err != nil {
			return err
		}
	}
	return WriteAtomic(filepath.Join(root, "pending.json"), raw)
}
