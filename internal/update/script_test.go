package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// updateRepo builds a work checkout of a local "origin" holding the real
// scripts/update.sh, with a stub make first on PATH, and returns what a test
// needs to run the script. The origin URL is rewritten per test.
type updateRepo struct {
	root, origin, work, bin, state string
	fakeURL                        string // what the wrapper reports as origin's URL
}

func newUpdateRepo(t *testing.T, branch string) *updateRepo {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("update.sh is Unix only")
	}
	for _, tool := range []string{"git", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "update.sh"))
	if err != nil {
		t.Fatal(err)
	}
	r := updateRepo{root: t.TempDir()}
	r.origin, r.work = filepath.Join(r.root, "origin.git"), filepath.Join(r.root, "work")
	r.bin, r.state = filepath.Join(r.root, "bin"), filepath.Join(r.root, "state")
	gitRun(t, r.root, "init", "--bare", "-b", "release", r.origin)
	gitRun(t, r.root, "clone", r.origin, r.work)
	gitRun(t, r.work, "checkout", "-b", branch)
	if err := os.MkdirAll(filepath.Join(r.work, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.work, "scripts", "update.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, r.work, "add", ".")
	gitRun(t, r.work, "commit", "-m", "one")
	gitRun(t, r.work, "push", "origin", branch)
	if err := os.MkdirAll(r.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// The script reads origin's URL with "remote get-url", which names the
	// real GitHub address in production. The test fetches from a local bare
	// repository, so a wrapper answers that one question with a chosen URL and
	// hands everything else to the real git.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nif [ \"$1\" = remote ] && [ \"$2\" = get-url ]; then echo \"$FAKE_ORIGIN_URL\"; exit 0; fi\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(r.bin, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return &r
}

// stubMake installs a make that exits with code, as GNU make does (2) on a
// build error.
func (r *updateRepo) stubMake(t *testing.T, code string) {
	t.Helper()
	body := "#!/bin/sh\nexit " + code + "\n"
	if err := os.WriteFile(filepath.Join(r.bin, "make"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// run runs the script and returns its exit code.
func (r *updateRepo) run(t *testing.T, branch string) int {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(r.work, "scripts", "update.sh"), branch)
	cmd.Dir = r.work
	cmd.Env = append(os.Environ(), "PATH="+r.bin+":"+os.Getenv("PATH"), "XDG_STATE_HOME="+r.state, "FAKE_ORIGIN_URL="+r.fakeURL)
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	t.Fatal(err)
	return -1
}

func (r *updateRepo) log(t *testing.T) string {
	b, _ := os.ReadFile(filepath.Join(r.state, "atlas-commander", "update.log"))
	return string(b)
}

// useRealOrigin sets the URL the script will see for origin.
func (r *updateRepo) useRealOrigin(t *testing.T, url string) {
	t.Helper()
	r.fakeURL = url
}

// GNU make exits 2 on a build error, and 2 is the script's own "rebuilt but not
// the newest". If make's 2 passed through, the app would tell a user whose
// build broke that the update merely was not new. A build failure must be 1,
// success on a level checkout 0, and a rebuild-only fallback 2.
func TestUpdateScriptNormalisesBuildFailure(t *testing.T) {
	r := newUpdateRepo(t, "release")
	r.useRealOrigin(t, "https://github.com/EternalCoder454/atlas-commander.git")

	r.stubMake(t, "2")
	if got := r.run(t, "release"); got != 1 {
		t.Errorf("make fails: got exit %d, want 1\n%s", got, r.log(t))
	}
	r.stubMake(t, "0")
	if got := r.run(t, "release"); got != 0 {
		t.Errorf("make works: got exit %d, want 0\n%s", got, r.log(t))
	}

	// Uncommitted work means rebuild only: 2 on success, 1 when that fails.
	if err := os.WriteFile(filepath.Join(r.work, "scratch"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := r.run(t, "release"); got != 2 {
		t.Errorf("dirty tree, build works: got exit %d, want 2\n%s", got, r.log(t))
	}
	r.stubMake(t, "2")
	if got := r.run(t, "release"); got != 1 {
		t.Errorf("dirty tree, build fails: got exit %d, want 1\n%s", got, r.log(t))
	}
}

// Switching away from somebody's own branch would strand their work, so the
// script must refuse, and say why, rather than check out release.
func TestUpdateScriptRefusesOtherBranch(t *testing.T) {
	r := newUpdateRepo(t, "feature")
	r.useRealOrigin(t, "git@github.com:EternalCoder454/atlas-commander.git")
	r.stubMake(t, "0")
	if got := r.run(t, "release"); got != 1 {
		t.Errorf("feature branch: got exit %d, want 1\n%s", got, r.log(t))
	}
	if !strings.Contains(r.log(t), "another branch") {
		t.Errorf("got log %q, want it to mention another branch", r.log(t))
	}
	if b, _ := git(r.work, "rev-parse", "--abbrev-ref", "HEAD"); b != "feature" {
		t.Errorf("got branch %q, want feature left alone", b)
	}
}

// "Update" builds and installs whatever origin serves, so an origin that is
// not the Atlas Commander repository must be refused, and the real name must
// be accepted over https and ssh, with or without .git.
func TestUpdateScriptChecksOrigin(t *testing.T) {
	r := newUpdateRepo(t, "release")
	r.stubMake(t, "0")
	r.useRealOrigin(t, "https://github.com/someone/else.git")
	if got := r.run(t, "release"); got != 1 {
		t.Errorf("foreign origin: got exit %d, want 1\n%s", got, r.log(t))
	}
	for _, url := range []string{
		"https://github.com/EternalCoder454/atlas-commander",
		"https://github.com/EternalCoder454/atlas-commander.git",
		"git@github.com:EternalCoder454/atlas-commander.git",
		"ssh://git@github.com/EternalCoder454/atlas-commander",
	} {
		r.useRealOrigin(t, url)
		if got := r.run(t, "release"); got != 0 {
			t.Errorf("origin %s: got exit %d, want 0\n%s", url, got, r.log(t))
		}
	}
}
