package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

type pruneFixture struct {
	dir     string
	public  string
	private ed25519.PrivateKey
}

func newPruneFixture(t *testing.T) pruneFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pruneFixture{t.TempDir(), hex.EncodeToString(public), private}
}

func (f pruneFixture) publish(t *testing.T, platform string, sequence int64) clientrelease.Release {
	t.Helper()
	data := []byte(platform + string(rune('0'+sequence)))
	r := testRelease(data)
	r.Platform, r.Filename, r.Sequence = platform, clientrelease.Filename(platform), sequence
	destination := r.ArtifactPath(f.dir)
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0600); err != nil {
		t.Fatal(err)
	}
	signed, err := clientrelease.Sign(r, f.private)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishCatalog(f.dir, f.public, signed); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPruneRetainsTwoVersionsPerPlatformAndIsIdempotent(t *testing.T) {
	f := newPruneFixture(t)
	var old, kept []clientrelease.Release
	for _, platform := range []string{"windows-amd64", "android-arm64", "android-amd64"} {
		old = append(old, f.publish(t, platform, 1))
		kept = append(kept, f.publish(t, platform, 2), f.publish(t, platform, 3))
	}
	catalogBefore, _ := os.ReadFile(filepath.Join(f.dir, "godot-releases.json"))
	preview, err := pruneArtifacts(f.dir, f.public, false)
	if err != nil || !preview.DryRun || len(preview.Candidates) != 3 || preview.Retained != 6 || preview.Removed != 0 {
		t.Fatal(preview, err)
	}
	for _, release := range old {
		if _, err := os.Stat(release.ArtifactPath(f.dir)); err != nil {
			t.Fatal("dry run deleted an artifact", err)
		}
	}
	report, err := pruneArtifacts(f.dir, f.public, true)
	if err != nil || report.Removed != 3 || report.ReclaimedBytes != preview.CandidateBytes || report.ReclaimedBytes == 0 {
		t.Fatal(report, err)
	}
	for _, release := range kept {
		file, err := clientrelease.VerifyFile(release.ArtifactPath(f.dir), release)
		if err != nil {
			t.Fatal("retained artifact changed", err)
		}
		file.Close()
	}
	for _, release := range old {
		if _, err := os.Stat(filepath.Dir(release.ArtifactPath(f.dir))); !os.IsNotExist(err) {
			t.Fatal("orphan directory still exists", err)
		}
	}
	report, err = pruneArtifacts(f.dir, f.public, true)
	if err != nil || report.Removed != 0 || report.ReclaimedBytes != 0 {
		t.Fatal("cleanup was not idempotent", report, err)
	}
	catalogAfter, _ := os.ReadFile(filepath.Join(f.dir, "godot-releases.json"))
	if string(catalogBefore) != string(catalogAfter) {
		t.Fatal("cleanup rewrote signed catalog")
	}
}

func TestPruneValidationFailureNeverDeletesCandidates(t *testing.T) {
	for _, damage := range []string{"catalog", "empty", "signature", "missing-retained", "changed-retained", "changed-orphan", "wrong-key"} {
		t.Run(damage, func(t *testing.T) {
			f := newPruneFixture(t)
			old := f.publish(t, "windows-amd64", 1)
			f.publish(t, "windows-amd64", 2)
			current := f.publish(t, "windows-amd64", 3)
			catalogPath := filepath.Join(f.dir, "godot-releases.json")
			switch damage {
			case "catalog":
				mustWritePrune(t, catalogPath, []byte("broken"))
			case "empty":
				mustWritePrune(t, catalogPath, []byte(`{"releases":[]}`))
			case "signature":
				raw, _ := os.ReadFile(catalogPath)
				var catalog clientrelease.Catalog
				if err := json.Unmarshal(raw, &catalog); err != nil {
					t.Fatal(err)
				}
				catalog.Releases[0].Signature = "broken"
				raw, _ = json.Marshal(catalog)
				mustWritePrune(t, catalogPath, raw)
			case "missing-retained":
				if err := os.Remove(current.ArtifactPath(f.dir)); err != nil {
					t.Fatal(err)
				}
			case "changed-retained":
				mustWritePrune(t, current.ArtifactPath(f.dir), []byte("corrupt"))
			case "changed-orphan":
				mustWritePrune(t, old.ArtifactPath(f.dir), []byte("corrupt"))
			case "wrong-key":
				f.public = newPruneFixture(t).public
			}
			report, err := pruneArtifacts(f.dir, f.public, true)
			if err == nil || report.Removed != 0 {
				t.Fatal("validation failure allowed deletion", report, err)
			}
			if _, err := os.Stat(old.ArtifactPath(f.dir)); err != nil {
				t.Fatal("candidate removed before validation completed", err)
			}
		})
	}
}

func TestPrunePreservesUnknownContent(t *testing.T) {
	f := newPruneFixture(t)
	old := f.publish(t, "windows-amd64", 1)
	f.publish(t, "windows-amd64", 2)
	f.publish(t, "windows-amd64", 3)
	note := filepath.Join(filepath.Dir(old.ArtifactPath(f.dir)), "operator-notes.txt")
	mustWritePrune(t, note, []byte("keep"))
	mustWritePrune(t, filepath.Join(f.dir, "godot", "unrelated.txt"), []byte("keep"))
	report, err := pruneArtifacts(f.dir, f.public, true)
	if err != nil || report.Removed != 0 || len(report.Skipped) != 2 {
		t.Fatal(report, err)
	}
	if _, err := os.Stat(note); err != nil {
		t.Fatal(err)
	}
}

func TestPruneDoesNotFollowLinks(t *testing.T) {
	for _, retainedLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "orphan", true: "retained"}[retainedLink], func(t *testing.T) {
			f := newPruneFixture(t)
			old := f.publish(t, "windows-amd64", 1)
			f.publish(t, "windows-amd64", 2)
			current := f.publish(t, "windows-amd64", 3)
			target := old
			if retainedLink {
				target = current
			}
			dir := filepath.Dir(target.ArtifactPath(f.dir))
			outside := filepath.Join(t.TempDir(), "payload")
			if err := os.Rename(dir, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, dir); err != nil {
				t.Skip("symlink privilege unavailable:", err)
			}
			report, err := pruneArtifacts(f.dir, f.public, true)
			if report.Removed != 0 || (retainedLink && err == nil) || (!retainedLink && err != nil) {
				t.Fatal(report, err)
			}
			if _, err := os.Stat(filepath.Join(outside, target.Filename)); err != nil {
				t.Fatal("cleanup followed link", err)
			}
		})
	}
}

func mustWritePrune(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
