package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

// A successful write must replace the contents and leave no temp file behind.
func TestWriteFileReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	for _, body := range []string{"one", "two"} {
		if err := WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Fatalf("got %q, want %q", b, body)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("got %d entries, want only the target", len(ents))
	}
}

// A failed rename (target is a non-empty directory) must remove the temp file,
// or every failed save would leave litter next to the user's settings.
func TestWriteFileCleansTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("x"), 0o600); err == nil {
		t.Fatal("got nil error, want a rename failure")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("got %d entries, want only the directory", len(ents))
	}
}
