package update

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeCheckout makes a directory the detection accepts as a checkout.
func fakeCheckout(t *testing.T, root string) string {
	t.Helper()
	checkout := filepath.Join(root, "atlas-commander")
	if err := os.MkdirAll(filepath.Join(checkout, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "Makefile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return checkout
}

// The install recipes write the checkout, the installed binary and the built
// commit; the app must read all three back.
func TestDetectReadsInstallRecord(t *testing.T) {
	root := t.TempDir()
	checkout := fakeCheckout(t, root)
	exe := filepath.Join(root, "prefix", "bin", "atlas-commander")
	record := filepath.Join(root, "source")
	text := "source=" + checkout + "\nbinary=" + exe + "\ncommit=abc123\n"
	if err := os.WriteFile(record, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	in := detect(record, exe, neverOwned)
	if in.Kind != FromSource || in.Source != checkout || in.Commit != "abc123" {
		t.Errorf("got %+v, want a source install of %s built at abc123", in, checkout)
	}
}

// A checkout recorded for ~/.local must not claim a distro package in /usr:
// "Update" would then rebuild into the wrong place and leave the package that
// is actually running untouched.
func TestDetectIgnoresRecordForAnotherBinary(t *testing.T) {
	root := t.TempDir()
	checkout := fakeCheckout(t, root)
	record := filepath.Join(root, "source")
	text := "source=" + checkout + "\nbinary=" + filepath.Join(root, "local", "bin", "atlas-commander") + "\n"
	if err := os.WriteFile(record, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	owner := func(path string) (string, string, bool) { return "dnf", "atlas-commander", true }
	in := detect(record, "/usr/bin/atlas-commander", owner)
	if in.Kind != FromPackage {
		t.Errorf("got kind %v, want FromPackage", in.Kind)
	}
	if got := detect(record, "/opt/else/atlas-commander", neverOwned); got.Kind != Standalone {
		t.Errorf("unrelated binary: got kind %v, want Standalone", got.Kind)
	}
}

// The recorded binary may be reached through a symlink (a launcher in
// ~/.local/bin); the running executable is resolved, so the record must be too.
func TestDetectResolvesSymlinkedBinary(t *testing.T) {
	root := t.TempDir()
	checkout := fakeCheckout(t, root)
	real := filepath.Join(root, "prefix", "bin", "atlas-commander")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here")
	}
	record := filepath.Join(root, "source")
	if err := os.WriteFile(record, []byte("source="+checkout+"\nbinary="+link+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if in := detect(record, real, neverOwned); in.Kind != FromSource {
		t.Errorf("got kind %v, want FromSource", in.Kind)
	}
}

// A record from before the binary was written down is trusted, unless a
// package owns what is running, so existing installs keep updating.
func TestDetectTrustsOldRecordUnlessPackageOwned(t *testing.T) {
	root := t.TempDir()
	checkout := fakeCheckout(t, root)
	record := filepath.Join(root, "source")
	if err := os.WriteFile(record, []byte(checkout+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if in := detect(record, "/home/u/.local/bin/atlas-commander", neverOwned); in.Kind != FromSource {
		t.Errorf("old record: got kind %v, want FromSource", in.Kind)
	}
	owner := func(string) (string, string, bool) { return "apt", "atlas-commander", true }
	if in := detect(record, "/usr/bin/atlas-commander", owner); in.Kind != FromPackage {
		t.Errorf("old record, packaged binary: got kind %v, want FromPackage", in.Kind)
	}
}
