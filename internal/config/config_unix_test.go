//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Settings may hold a claude path and key variable name; keep them private.
func TestSaveFileMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(path, Defaults()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("got mode %o, want 600", got)
	}
}
