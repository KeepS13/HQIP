package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"

	"github.com/KeepS13/HQIP/internal/clientrelease"
)

var artifactDirectory = regexp.MustCompile(`^[0-9a-f]{64}$`)

type pruneArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type pruneReport struct {
	DryRun         bool            `json:"dryRun"`
	Retained       int             `json:"retained"`
	Candidates     []pruneArtifact `json:"candidates"`
	CandidateBytes int64           `json:"candidateBytes"`
	Removed        int             `json:"removed"`
	ReclaimedBytes int64           `json:"reclaimedBytes"`
	Skipped        []string        `json:"skipped"`
}

// pruneArtifacts requires the same exclusive release operator as publication.
// It verifies the entire retained catalog and all candidates before deleting
// anything. Root-relative, non-recursive removals cannot traverse outside the
// artifact tree; unknown files, links and mixed directories are never removed.
// A failed/partial cleanup is safe to retry. It never edits the signed catalog.
func pruneArtifacts(directory, public string, apply bool) (pruneReport, error) {
	report := pruneReport{DryRun: !apply}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return report, err
	}
	defer root.Close()
	retained, err := retainedArtifacts(root, public)
	if err != nil {
		return report, err
	}
	report.Retained = len(retained)
	if err = planPrune(root, retained, &report); err != nil || !apply {
		return report, err
	}
	for _, item := range report.Candidates {
		if err = root.Remove(item.Path); err != nil {
			return report, err
		}
		report.Removed++
		report.ReclaimedBytes += item.Size
		if err = root.Remove(path.Dir(item.Path)); err != nil {
			return report, err
		}
	}
	return report, nil
}

func retainedArtifacts(root *os.Root, public string) (map[string]bool, error) {
	if err := requireDirectory(root, "godot"); err != nil {
		return nil, err
	}
	info, err := root.Lstat("godot-releases.json")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, fmt.Errorf("invalid release catalog file")
	}
	raw, err := root.ReadFile("godot-releases.json")
	if err != nil {
		return nil, err
	}
	var catalog clientrelease.Catalog
	if err = json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	if len(catalog.Releases) == 0 || len(catalog.Releases) > 16 {
		return nil, fmt.Errorf("refusing cleanup without a valid retained catalog")
	}
	retained := map[string]bool{}
	for _, signed := range catalog.Releases {
		release, err := signed.Verify(public)
		if err != nil {
			return nil, err
		}
		dir := path.Join("godot", release.SHA256)
		if err := requireDirectory(root, dir); err != nil {
			return nil, err
		}
		if _, err := verifyPruneFile(root, path.Join(dir, release.Filename), release.SHA256, release.Size); err != nil {
			return nil, err
		}
		retained[release.SHA256] = true
	}
	return retained, nil
}

func requireDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing linked or non-directory artifact path: %s", name)
	}
	return nil
}

func planPrune(root *os.Root, retained map[string]bool, report *pruneReport) error {
	entries, err := fs.ReadDir(root.FS(), "godot")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if retained[entry.Name()] {
			continue
		}
		dir := path.Join("godot", entry.Name())
		if !artifactDirectory.MatchString(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			report.Skipped = append(report.Skipped, dir)
			continue
		}
		files, err := fs.ReadDir(root.FS(), dir)
		if err != nil {
			return err
		}
		// Published digest directories contain exactly one known package. Extra
		// content may belong to an operator or an interrupted build; preserve it.
		if len(files) != 1 || !knownPackage(files[0].Name()) || !files[0].Type().IsRegular() {
			report.Skipped = append(report.Skipped, dir)
			continue
		}
		name := path.Join(dir, files[0].Name())
		size, err := verifyPruneFile(root, name, entry.Name(), 0)
		if err != nil {
			return err
		}
		report.Candidates = append(report.Candidates, pruneArtifact{name, entry.Name(), size})
		report.CandidateBytes += size
	}
	return nil
}

func knownPackage(name string) bool {
	for _, platform := range []string{"windows-amd64", "android-arm64", "android-amd64"} {
		if name == clientrelease.Filename(platform) {
			return true
		}
	}
	return false
}

// expectedSize == 0 checks an orphan against its content-addressed directory.
// Retained packages additionally require the exact size in the signed release.
func verifyPruneFile(root *os.Root, name, digest string, expectedSize int64) (int64, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > clientrelease.MaxPackageSize || (expectedSize != 0 && info.Size() != expectedSize) {
		return 0, fmt.Errorf("invalid artifact file: %s", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, clientrelease.MaxPackageSize+1))
	if err != nil {
		return 0, err
	}
	if size != info.Size() || hex.EncodeToString(hash.Sum(nil)) != digest {
		return 0, fmt.Errorf("artifact checksum mismatch: %s", name)
	}
	return size, nil
}

// Reports remain outside the database. Emit partial progress even on error so
// operators can distinguish a failed validation from an interrupted cleanup.
func pruneAndReport(directory, public string, apply bool) error {
	report, err := pruneArtifacts(directory, public, apply)
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil && err == nil {
		err = encodeErr
	}
	return err
}
