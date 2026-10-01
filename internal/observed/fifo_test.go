//go:build unix

package observed

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestReadRefusesFIFO matters because opening a FIFO for reading blocks until
// a writer appears, which would hang the reader for good.
func TestReadRefusesFIFO(t *testing.T) {
	root, _ := setup(t, "basic.jsonl", "-tmp-a", "s")
	fifo := filepath.Join(root, "-tmp-a", "pipe.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("cannot make a FIFO here:", err)
	}
	done := make(chan error, 1)
	go func() { _, err := Read(fifo, 0); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("got nil error, want a refusal")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Read blocked on a FIFO, want a quick refusal")
	}
}
