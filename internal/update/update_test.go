package update

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A beta is older than its release and numbers compare as numbers: getting
// either wrong tells people on the beta channel they are up to date when the
// release is out, or offers 0.9.0 as newer than 0.10.0.
func TestCompareOrdersVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.1.0", 1},
		{"0.1.0", "0.2.0", -1},
		{"0.10.0", "0.9.0", 1},
		{"v0.2.0", "0.2.0", 0},
		{"0.2.0-beta", "0.2.0", -1},
		{"0.2.0", "0.2.0-beta", 1},
		{"0.2.0-beta", "0.2.0-beta", 0},
		{"0.2.0-beta", "0.1.0", 1},
		{"0.1.0", "0.2.0-beta", -1},
		{"0.2.0-beta.2", "0.2.0-beta.10", -1},
		{"1.0", "1.0.0", 0},
	}
	for _, c := range cases {
		got := Compare(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("Compare(%q, %q): got %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

// The update prompt shows these lines, so the right section must be picked,
// continuation lines joined, and the list capped.
func TestChangelogExtractsOneVersion(t *testing.T) {
	md := "# What's new\n\nintro\n\n## 0.2.0\n\n- First thing\n- Second\n  that wraps\n\n## 0.1.0-beta\n\n- Old\n"
	if got, want := Changelog(md, "0.2.0"), []string{"First thing", "Second that wraps"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := Changelog(md, "0.1.0-beta"), []string{"Old"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// The beta channel's VERSION has a suffix its heading may lack.
	if got, want := Changelog(md, "0.2.0-beta"), []string{"First thing", "Second that wraps"}; !slices.Equal(got, want) {
		t.Errorf("suffix: got %q, want %q", got, want)
	}
	if got := Changelog(md, "9.9.9"); got != nil {
		t.Errorf("unknown version: got %q, want nothing", got)
	}
	if got := Changelog(md, ""); got != nil {
		t.Errorf("empty version: got %q, want nothing", got)
	}
	var long strings.Builder
	long.WriteString("## 1.0.0\n")
	for range 20 {
		long.WriteString("- line\n")
	}
	if got := len(Changelog(long.String(), "1.0.0")); got != maxChangelogBullets {
		t.Errorf("cap: got %d bullets, want %d", got, maxChangelogBullets)
	}
}

func fakeRaw(t *testing.T, files map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	old := RawBase
	RawBase = srv.URL
	t.Cleanup(func() { RawBase = old; srv.Close() })
}

// The remote check is what packaged, tarball and Windows installs rely on.
func TestCheckRemoteFindsNewerVersion(t *testing.T) {
	fakeRaw(t, map[string]string{
		"/release/VERSION":      "0.2.0\n",
		"/release/CHANGELOG.md": "## 0.2.0\n\n- Shiny\n",
		"/beta/VERSION":         "0.3.0-beta\n",
	})
	std := Install{Kind: Standalone}

	info, err := Check(std, ChannelRelease, "0.1.0-beta")
	if err != nil || !info.Available || info.Version != "0.2.0" || !slices.Equal(info.Changes, []string{"Shiny"}) {
		t.Errorf("newer: got %+v, %v, want 0.2.0 with its changes", info, err)
	}
	info, err = Check(std, ChannelRelease, "0.2.0")
	if err != nil || info.Available {
		t.Errorf("same: got %+v, %v, want up to date", info, err)
	}
	// A beta is older than the release made from it.
	info, _ = Check(std, ChannelRelease, "0.2.0-beta")
	if !info.Available {
		t.Errorf("beta to release: got %+v, want available", info)
	}
	info, _ = Check(std, ChannelBeta, "0.2.0")
	if !info.Available || info.Version != "0.3.0-beta" {
		t.Errorf("beta channel: got %+v, want 0.3.0-beta", info)
	}
}

// Offline and broken answers are reported in a plain sentence, never as an
// available update.
func TestCheckRemoteReportsFailures(t *testing.T) {
	fakeRaw(t, map[string]string{"/release/VERSION": "  \n"})
	if _, err := Check(Install{Kind: Standalone}, ChannelRelease, "0.1.0"); err != ErrNoVersion {
		t.Errorf("empty VERSION: got %v, want %v", err, ErrNoVersion)
	}
	if _, err := Check(Install{Kind: Standalone}, ChannelBeta, "0.1.0"); err != ErrOffline {
		t.Errorf("404: got %v, want %v", err, ErrOffline)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A source install compares commits, which needs no network: the "origin" here
// is a local bare repository.
func TestCheckGitSeesNewCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	origin, work, other := filepath.Join(root, "origin.git"), filepath.Join(root, "work"), filepath.Join(root, "other")
	gitRun(t, root, "init", "--bare", "-b", "release", origin)
	gitRun(t, root, "clone", origin, work)
	gitRun(t, work, "checkout", "-b", "release")
	if err := os.WriteFile(filepath.Join(work, "VERSION"), []byte("0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-m", "one")
	gitRun(t, work, "push", "origin", "release")

	in := Install{Kind: FromSource, Source: work}
	info, err := Check(in, ChannelRelease, "0.1.0")
	if err != nil || info.Available {
		t.Fatalf("level with origin: got %+v, %v, want up to date", info, err)
	}

	gitRun(t, root, "clone", origin, other)
	gitRun(t, other, "checkout", "release")
	_ = os.WriteFile(filepath.Join(other, "VERSION"), []byte("0.2.0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(other, "CHANGELOG.md"), []byte("## 0.2.0\n- New\n"), 0o644)
	gitRun(t, other, "add", ".")
	gitRun(t, other, "commit", "-m", "two")
	gitRun(t, other, "push", "origin", "release")

	info, err = Check(in, ChannelRelease, "0.1.0")
	if err != nil || !info.Available || info.Version != "0.2.0" || !slices.Equal(info.Changes, []string{"New"}) {
		t.Errorf("behind origin: got %+v, %v, want 0.2.0 available", info, err)
	}
}

func neverOwned(string) (string, string, bool) { return "", "", false }

// What make install records must be what the app reads back as a source install,
// and a stale record (the checkout was deleted) must not be trusted.
func TestDetectRecognisesRecordedCheckout(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "atlas-commander")
	if err := os.MkdirAll(filepath.Join(checkout, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "justfile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(root, "source")
	if err := os.WriteFile(record, []byte(checkout+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "prefix", "bin", "atlas-commander")

	in := detect(record, exe, neverOwned)
	if in.Kind != FromSource || in.Source != checkout {
		t.Errorf("got %+v, want a source install of %s", in, checkout)
	}
	if got, want := in.Prefix(), filepath.Join(root, "prefix"); got != want {
		t.Errorf("prefix: got %q, want %q", got, want)
	}

	// Running the binary built inside the checkout must not pick the checkout as
	// the install prefix.
	dev := detect(record, filepath.Join(checkout, "bin", "atlas-commander"), neverOwned)
	if dev.Prefix() != "" {
		t.Errorf("dev run: got prefix %q, want none", dev.Prefix())
	}

	if err := os.RemoveAll(checkout); err != nil {
		t.Fatal(err)
	}
	if got := detect(record, exe, neverOwned); got.Kind != Standalone {
		t.Errorf("stale record: got kind %v, want Standalone", got.Kind)
	}
}

// A binary a package manager owns must be left to it: replacing its files
// would corrupt the package database.
func TestDetectRecognisesPackageOwner(t *testing.T) {
	owner := func(path string) (string, string, bool) { return "dnf", "atlas-commander", path == "/usr/bin/atlas-commander" }
	in := detect(filepath.Join(t.TempDir(), "none"), "/usr/bin/atlas-commander", owner)
	if in.Kind != FromPackage || in.Manager != "dnf" || in.Package != "atlas-commander" {
		t.Errorf("got %+v, want a dnf package", in)
	}
	if in.SelfUpdatable() {
		t.Error("a packaged install must not update itself")
	}
	if got := detect(filepath.Join(t.TempDir(), "none"), "/opt/ac/bin/atlas-commander", owner); got.Kind != Standalone {
		t.Errorf("unowned: got kind %v, want Standalone", got.Kind)
	}
}

// The owner queries' output formats are what the detection depends on.
func TestOwnerOutputParsing(t *testing.T) {
	if got := parsePacman("/usr/bin/atlas-commander is owned by atlas-commander-git 0.1.0-1"); got != "atlas-commander-git" {
		t.Errorf("pacman: got %q", got)
	}
	if got := parsePacman("error: No package owns /x"); got != "" {
		t.Errorf("pacman unowned: got %q, want empty", got)
	}
	if got := parseDpkg("atlas-commander: /usr/bin/atlas-commander"); got != "atlas-commander" {
		t.Errorf("dpkg: got %q", got)
	}
	if got := firstField("atlas-commander\n"); got != "atlas-commander" {
		t.Errorf("rpm: got %q", got)
	}
}

// The command is shown to be pasted into a terminal, so it must be right for
// each manager and must not carry an odd package name through.
func TestCommandPerPackageManager(t *testing.T) {
	cases := []struct{ mgr, pkg, aur, want string }{
		{"dnf", "atlas-commander", "", "sudo dnf upgrade atlas-commander"},
		{"yum", "atlas-commander", "", "sudo yum update atlas-commander"},
		{"zypper", "atlas-commander", "", "sudo zypper update atlas-commander"},
		{"apt", "atlas-commander", "", "sudo apt update && sudo apt install --only-upgrade atlas-commander"},
		{"pacman", "atlas-commander-git", "paru", "paru -Syu atlas-commander-git"},
		{"pacman", "atlas-commander", "", "git clone " + RepoURL + ".git && cd atlas-commander/packaging && makepkg -si"},
		{"dnf", "x; rm -rf ~", "", "sudo dnf upgrade atlas-commander"},
		{"", "atlas-commander", "", ""},
	}
	for _, c := range cases {
		if got := Command(c.mgr, c.pkg, c.aur); got != c.want {
			t.Errorf("Command(%q, %q, %q): got %q, want %q", c.mgr, c.pkg, c.aur, got, c.want)
		}
	}
}

// Run must pass the channel and prefix to the script and map its exit codes to
// the three outcomes the dialog tells apart. A stub script stands in for the
// real one, which would pull from GitHub and rebuild.
func TestRunMapsScriptExitCodes(t *testing.T) {
	if !CanSelfInstall {
		t.Skip("source installs update themselves only on Unix")
	}
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(checkout, "args")
	script := "#!/bin/bash\necho \"$@\" > " + seen + "\nexit \"${FAKE_EXIT:-0}\"\n"
	if err := os.WriteFile(filepath.Join(checkout, "scripts", "update.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	in := Install{Kind: FromSource, Source: checkout, Binary: "/opt/ac/bin/atlas-commander"}

	t.Setenv("FAKE_EXIT", "0")
	if err := Run(in, ChannelBeta); err != nil {
		t.Errorf("exit 0: got %v, want nil", err)
	}
	if b, _ := os.ReadFile(seen); strings.TrimSpace(string(b)) != "beta /opt/ac" {
		t.Errorf("args: got %q, want %q", b, "beta /opt/ac")
	}
	t.Setenv("FAKE_EXIT", "2")
	if err := Run(in, ChannelRelease); err != ErrNotUpdated {
		t.Errorf("exit 2: got %v, want %v", err, ErrNotUpdated)
	}
	t.Setenv("FAKE_EXIT", "1")
	if err := Run(in, ChannelRelease); err == nil || err == ErrNotUpdated {
		t.Errorf("exit 1: got %v, want a build failure", err)
	}
	if err := Run(Install{Kind: Standalone}, ChannelRelease); err == nil {
		t.Error("a standalone install: got nil, want an error")
	}
}
