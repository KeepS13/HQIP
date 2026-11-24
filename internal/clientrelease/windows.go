package clientrelease

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var WindowsFiles = []string{"HQIP-UI.exe", "HQIP-UI.pck", "hqip-runtime.exe", "hqip-helper.exe", "sing-box.exe", "libcronet.dll", "THIRD-PARTY-NOTICES.txt", "sing-box-LICENSE"}

// ExtractWindows accepts exactly the fixed application payload, without path
// components, links or executable installer scripts. It never touches the live
// version. On failure, only its own freshly created staging directory is removed.
func ExtractWindows(archive, destination string, release Release) error {
	if release.Platform != "windows-amd64" {
		return errors.New("wrong update platform")
	}
	file, err := VerifyFile(archive, release)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := zip.NewReader(file, release.Size)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, name := range WindowsFiles {
		allowed[name] = true
	}
	seen := map[string]bool{}
	var total uint64
	for _, entry := range reader.File {
		if !allowed[entry.Name] || seen[entry.Name] || !entry.Mode().IsRegular() {
			return errors.New("invalid update archive layout")
		}
		seen[entry.Name] = true
		total += entry.UncompressedSize64
		if entry.UncompressedSize64 > 300<<20 || total > 700<<20 {
			return errors.New("update archive too large")
		}
	}
	if len(seen) != len(allowed) {
		return errors.New("incomplete update archive")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(destination)
		}
	}()
	for _, entry := range reader.File {
		if err = extractEntry(entry, filepath.Join(destination, entry.Name)); err != nil {
			return err
		}
	}
	success = true
	return nil
}
func extractEntry(entry *zip.File, path string) error {
	in, err := entry.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(in, int64(entry.UncompressedSize64)+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != int64(entry.UncompressedSize64) {
		return errors.New("update entry size mismatch")
	}
	return nil
}
