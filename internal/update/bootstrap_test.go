package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A machine that installed a prebuilt tarball often lacks the toolchain. The
// update must name what is missing and the one command that installs it, not
// fail later with a page of compiler errors.
func TestMissingToolsNamesWhatToInstall(t *testing.T) {
	have := func(names ...string) bool {
		for _, n := range names {
			if n == "go" || n == "make" || n == "just" {
				return false
			}
		}
		return true
	}
	missing := missingFrom(have, func(lib string) bool { return lib != "Qt6Widgets" })
	var labels []string
	for _, m := range missing {
		labels = append(labels, m.label)
	}
	if want := []string{"Go", "make", "the Qt 6 development files"}; !slices.Equal(labels, want) {
		t.Fatalf("got %q, want %q", labels, want)
	}
	if got, want := installHint("dnf", missing), "sudo dnf install golang make qt6-qtbase-devel"; got != want {
		t.Errorf("dnf: got %q, want %q", got, want)
	}
	if got, want := installHint("apt", missing), "sudo apt install golang-go make qt6-base-dev"; got != want {
		t.Errorf("apt: got %q, want %q", got, want)
	}
	if got := installHint("emerge", missing); got != "" {
		t.Errorf("unknown manager: got %q, want nothing", got)
	}
}

// On Arch the compiler and make are both base-devel; the hint must not ask for
// it twice.
func TestInstallHintNamesEachPackageOnce(t *testing.T) {
	missing := missingFrom(func(...string) bool { return false }, func(string) bool { return false })
	if got := installHint("pacman", missing); strings.Count(got, "base-devel") != 1 {
		t.Errorf("got %q, want base-devel once", got)
	}
}

// Without pkg-config nothing can be asked about libraries, and a library
// reported missing for that reason would be a guess.
func TestMissingToolsSkipsLibrariesWithoutPkgConfig(t *testing.T) {
	have := func(names ...string) bool { return !slices.Contains(names, "pkg-config") }
	missing := missingFrom(have, func(string) bool { return false })
	for _, m := range missing {
		if m.pkgConfig != "" {
			t.Errorf("got %q in the list, want libraries left out without pkg-config", m.label)
		}
	}
}

// localOrigin is a bare repository with one commit on release, standing in
// for GitHub.
func localOrigin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	origin, work := filepath.Join(root, "origin.git"), filepath.Join(root, "work")
	gitRun(t, root, "init", "--bare", "-b", "release", origin)
	gitRun(t, root, "clone", origin, work)
	gitRun(t, work, "checkout", "-b", "release")
	_ = os.WriteFile(filepath.Join(work, "VERSION"), []byte("0.1.0\n"), 0o644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-m", "one")
	gitRun(t, work, "push", "origin", "release")
	return origin
}

// sandboxBootstrap points the data folder, the clone and the tool check at
// test values.
func sandboxBootstrap(t *testing.T, origin string, missing []buildTool) {
	t.Helper()
	t.Setenv("ATLAS_DATA_HOME", t.TempDir())
	oldURL, oldTools := cloneURL, missingTools
	cloneURL = origin
	missingTools = func() []buildTool { return missing }
	t.Cleanup(func() { cloneURL, missingTools = oldURL, oldTools })
}

// The first update of a tarball install fetches the source and hands back a
// source install for Run. It must not write the install record: make install
// does that once the build has worked. A record written before the build would
// make a failed first build look installed, and the check would then go by the
// clone and call the old binary up to date.
func TestBootstrapClonesWithoutRecording(t *testing.T) {
	origin := localOrigin(t)
	sandboxBootstrap(t, origin, nil)
	exe := filepath.Join(t.TempDir(), "bin", "atlas-commander")

	in, err := Bootstrap(Install{Kind: Standalone, Binary: exe, Writable: true}, ChannelRelease, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != FromSource || in.Source != SourceDir() || in.Binary != exe {
		t.Fatalf("got %+v, want a source install of %s for %s", in, SourceDir(), exe)
	}
	if _, err := git(in.Source, "rev-parse", "--git-dir"); err != nil {
		t.Fatalf("%s is not a checkout after Bootstrap", in.Source)
	}
	if _, err := os.Stat(SourceFile()); err == nil {
		t.Error("got an install record, want none until make install writes it")
	}
	// A second try, after a failed build, reuses the clone.
	if again, err := Bootstrap(Install{Kind: Standalone, Binary: exe}, ChannelRelease, func(string) {}); err != nil || again.Source != in.Source {
		t.Errorf("second Bootstrap: got %+v, %v, want the same clone", again, err)
	}
}

// A clone cut off halfway, or anything else left in the source folder, would
// be taken for a checkout and pulled in. It must be replaced.
func TestBootstrapReplacesWhatIsNotACheckout(t *testing.T) {
	origin := localOrigin(t)
	sandboxBootstrap(t, origin, nil)
	if err := os.MkdirAll(SourceDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(SourceDir(), "leftover"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Bootstrap(Install{Kind: Standalone}, ChannelRelease, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(SourceDir(), "leftover")); err == nil {
		t.Error("got the leftover file still there, want the folder replaced by a clone")
	}
}

// Missing tools are found before anything is downloaded, and reported as
// something to install rather than a failed update.
func TestBootstrapChecksToolsBeforeCloning(t *testing.T) {
	origin := localOrigin(t)
	sandboxBootstrap(t, origin, []buildTool{buildTools[1]})
	_, err := Bootstrap(Install{Kind: Standalone}, ChannelRelease, func(string) {})
	var setup *SetupError
	if !errors.As(err, &setup) || !strings.Contains(err.Error(), "Go") {
		t.Fatalf("got %v, want a SetupError naming Go", err)
	}
	if _, err := os.Stat(SourceDir()); err == nil {
		t.Error("got a source folder, want nothing fetched")
	}
}

// A tarball copy can install a new build over itself only from a writable
// prefix. Run from the folder it was unpacked into, or from a checkout's own
// bin/, "installing over it" would write into the folder above.
func TestStandaloneSelfUpdatesOnlyFromAPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows copies never update themselves")
	}
	root := t.TempDir()
	record := filepath.Join(root, "no-record")
	mkExe := func(path string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}

	installed := detect(record, mkExe(filepath.Join(root, "local", "bin", "atlas-commander")), neverOwned)
	if installed.Kind != Standalone || !installed.SelfUpdatable() {
		t.Errorf("installed in a prefix: got %+v, want a standalone copy that updates itself", installed)
	}
	unpacked := detect(record, mkExe(filepath.Join(root, "atlas-commander-0.1.0", "atlas-commander")), neverOwned)
	if unpacked.SelfUpdatable() {
		t.Errorf("unpacked folder: got %+v, want no self-update", unpacked)
	}
	if h, _ := Wording(unpacked); h != "Download the new version" {
		t.Errorf("unpacked folder: got heading %q, want the download wording", h)
	}
	checkout := fakeCheckout(t, root)
	if dev := detect(record, mkExe(filepath.Join(checkout, "bin", "atlas-commander")), neverOwned); dev.SelfUpdatable() {
		t.Errorf("checkout's bin/: got %+v, want no self-update", dev)
	}
	// Installed system-wide by root: in a prefix, but not this account's.
	locked := Install{Kind: Standalone, Binary: filepath.Join(root, "usr", "bin", "atlas-commander")}
	if h, _ := Wording(locked); h != "Update needs permission" {
		t.Errorf("unwritable prefix: got heading %q, want the permission wording", h)
	}
}

// The command reaches the terminal as one argument behind a fixed wrapper, so
// nothing in it is ever parsed as part of another shell's command line.
func TestTerminalPassesTheCommandAsAnArgument(t *testing.T) {
	cmd := "sudo dnf upgrade atlas-commander"
	argv := Terminal{"konsole", []string{"-e"}}.argv(cmd)
	if argv[0] != "-e" {
		t.Errorf("got %q, want it to start with the terminal's own flag", argv)
	}
	if argv[len(argv)-1] != cmd {
		t.Errorf("got %q, want the command whole as the last argument", argv)
	}
	if argv := (Terminal{"kitty", nil}).argv(cmd); argv[0] != "bash" {
		t.Errorf("got %q, want bash first when the terminal takes no flag", argv)
	}
}
