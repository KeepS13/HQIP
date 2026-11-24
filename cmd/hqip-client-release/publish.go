package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

// publishArtifact owns its temporary file. Readers see the full verified file
// only after rename; a changed source or interrupted copy never replaces it.
func publishArtifact(source, directory string, release clientrelease.Release) error {
	destination := release.ArtifactPath(directory)
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".release-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closed := out.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	verified, err := clientrelease.VerifyFile(out.Name(), release)
	if err != nil {
		return err
	}
	verified.Close()
	return os.Rename(out.Name(), destination)
}

// publishCatalog is for a single release operator. Publish packages first and
// replace the catalog last. Keep two sequences per platform so a downloader
// holding the preceding manifest can finish while a new version is published.
// The command prunes unreferenced files only after this atomic publication.
// Same-sequence replacement is allowed for unpublished preview rebuilds only;
// once distributed, every changed package must receive a higher sequence.
func publishCatalog(directory, public string, envelope clientrelease.Envelope) error {
	latest, err := envelope.Verify(public)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "godot-releases.json")
	var catalog clientrelease.Catalog
	raw, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(raw, &catalog); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	type entry struct {
		release clientrelease.Release
		signed  clientrelease.Envelope
	}
	entries := []entry{{latest, envelope}}
	for _, signed := range catalog.Releases {
		release, err := signed.Verify(public)
		if err != nil {
			return err
		}
		if release.Platform == latest.Platform {
			if release.Sequence > latest.Sequence {
				return fmt.Errorf("release sequence must not decrease")
			}
			if release.Sequence == latest.Sequence {
				continue
			}
		}
		entries = append(entries, entry{release, signed})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].release.Sequence > entries[j].release.Sequence })
	counts := map[string]int{}
	catalog.Releases = nil
	for _, item := range entries {
		if counts[item.release.Platform] < 2 {
			catalog.Releases = append(catalog.Releases, item.signed)
			counts[item.release.Platform]++
		}
	}
	raw, err = json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	return clientrelease.WriteAtomic(path, append(raw, '\n'))
}
