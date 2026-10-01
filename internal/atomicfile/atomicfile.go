// Package atomicfile replaces a file so readers see either the old contents
// or the new, never a truncated mix.
//
// Goroutines: stateless, safe from any goroutine. Platform: the directory
// fsync is Unix only and the rename retry is Windows only, in
// atomicfile_unix.go and atomicfile_windows.go.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile writes data to path through a temp file in the same directory:
// write, fsync, rename. The temp file is created 0600 and chmodded to perm
// before the rename. The temp file is removed on any failure. The parent
// directory is created (0755) if missing.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// Rename only orders the directory entry; flush first so the new name
	// never shows up before its contents.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if err := rename(tmp, path); err != nil {
		return err
	}
	ok = true
	// The data is already in place; a directory sync failure only weakens
	// durability, so it is not reported.
	syncDir(dir)
	return nil
}
