package clientrelease

import (
	"os"
	"path/filepath"
)

// WriteAtomic replaces a small pointer only after its contents have been synced.
// Temporary files belong to this call and are removed on every failure path.
func WriteAtomic(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".hqip-pointer-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	return os.Rename(temporary, path)
}
