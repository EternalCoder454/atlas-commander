package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A checkout of origin with one commit on release, plus a second commit pushed
// from elsewhere so the checkout is behind. Returns the checkout and the commit
// it is at.
func behindCheckout(t *testing.T) (work, atFirst string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	origin, work, other := filepath.Join(root, "origin.git"), filepath.Join(root, "work"), filepath.Join(root, "other")
	gitRun(t, root, "init", "--bare", "-b", "release", origin)
	gitRun(t, root, "clone", origin, work)
	gitRun(t, work, "checkout", "-b", "release")
	_ = os.WriteFile(filepath.Join(work, "VERSION"), []byte("0.1.0\n"), 0o644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-m", "one")
	gitRun(t, work, "push", "origin", "release")
	atFirst, _ = git(work, "rev-parse", "HEAD")

	gitRun(t, root, "clone", origin, other)
	gitRun(t, other, "checkout", "release")
	_ = os.WriteFile(filepath.Join(other, "VERSION"), []byte("0.2.0\n"), 0o644)
	gitRun(t, other, "add", ".")
	gitRun(t, other, "commit", "-m", "two")
	gitRun(t, other, "push", "origin", "release")
	return work, atFirst
}

// On a branch of their own, an update would switch away from the work, so the
// check must not offer one; it says why instead.
func TestCheckGitLeavesOtherBranchAlone(t *testing.T) {
	work, _ := behindCheckout(t)
	gitRun(t, work, "checkout", "-b", "my-feature")
	info, err := Check(Install{Kind: FromSource, Source: work}, ChannelRelease, "0.1.0")
	if err != nil || info.Available || info.Summary != "Your checkout is on another branch" {
		t.Errorf("got %+v, %v, want not available on another branch", info, err)
	}
}

// If the pull worked and the build after it failed, HEAD is new but the binary
// is old. The check must go by the commit that was built, or it would say
// "up to date" about a copy that is not.
func TestCheckGitComparesInstalledCommitNotHead(t *testing.T) {
	work, atFirst := behindCheckout(t)
	gitRun(t, work, "pull", "--ff-only", "origin", "release") // HEAD is now current

	level := Install{Kind: FromSource, Source: work}
	if info, err := Check(level, ChannelRelease, "0.1.0"); err != nil || info.Available {
		t.Fatalf("no record, HEAD current: got %+v, %v, want up to date", info, err)
	}
	old := Install{Kind: FromSource, Source: work, Commit: atFirst}
	info, err := Check(old, ChannelRelease, "0.1.0")
	if err != nil || !info.Available || info.Version != "0.2.0" {
		t.Errorf("built before the pull: got %+v, %v, want 0.2.0 available", info, err)
	}
	head, _ := git(work, "rev-parse", "HEAD")
	built := Install{Kind: FromSource, Source: work, Commit: head}
	if info, err := Check(built, ChannelRelease, "0.2.0"); err != nil || info.Available {
		t.Errorf("built at HEAD: got %+v, %v, want up to date", info, err)
	}
}
