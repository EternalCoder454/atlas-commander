package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	run(t, d, "init", "-q")
	os.WriteFile(filepath.Join(d, "a.txt"), []byte("a"), 0o644)
	run(t, d, "add", ".")
	run(t, d, "commit", "-q", "-m", "init")
	return d
}

// Each agent needs an isolated checkout on its own branch.
func TestCreateMakesWorktreeOnBranch(t *testing.T) {
	repo, root := newRepo(t), t.TempDir()
	p, b, err := Create(repo, root, "ab12", "Fix The Bug!!")
	if err != nil {
		t.Fatal(err)
	}
	if b != "atlas/fix-the-bug-ab12" || p != filepath.Join(root, "fix-the-bug-ab12") {
		t.Errorf("got %q %q, want atlas/fix-the-bug-ab12 under root", b, p)
	}
	if _, err := os.Stat(filepath.Join(p, "a.txt")); err != nil {
		t.Errorf("worktree has no checkout: %v", err)
	}
	if got := run(t, p, "rev-parse", "--abbrev-ref", "HEAD"); got != b {
		t.Errorf("branch: got %q, want %q", got, b)
	}
}

// Restarting an agent must come back to the same worktree, not fail.
func TestCreateReusesWorktreeAndBranch(t *testing.T) {
	repo, root := newRepo(t), t.TempDir()
	p1, _, _ := Create(repo, root, "01", "x")
	p2, _, err := Create(repo, root, "01", "x")
	if err != nil || p2 != p1 {
		t.Errorf("reuse: got %q, %v; want %q", p2, err, p1)
	}
	// Worktree removed but branch kept: a new Create reuses the branch.
	if err := Remove(repo, root, p1); err != nil {
		t.Fatal(err)
	}
	p3, b, err := Create(repo, root, "01", "x")
	if err != nil {
		t.Fatalf("reuse branch: %v", err)
	}
	if got := run(t, p3, "rev-parse", "--abbrev-ref", "HEAD"); got != b {
		t.Errorf("branch: got %q, want %q", got, b)
	}
}

// Names that fold or truncate to the same text must not share a branch or
// folder, or two agents would overwrite each other's files.
func TestCreateIDsKeepSimilarNamesApart(t *testing.T) {
	repo, root := newRepo(t), t.TempDir()
	long := strings.Repeat("a", 40)
	p1, b1, err1 := Create(repo, root, "aa11", long+"1")
	p2, b2, err2 := Create(repo, root, "bb22", long+"2")
	p3, b3, err3 := Create(repo, root, "cc33", "Fix Bug")
	p4, b4, err4 := Create(repo, root, "dd44", "fix-bug")
	for _, err := range []error{err1, err2, err3, err4} {
		if err != nil {
			t.Fatal(err)
		}
	}
	paths := map[string]bool{p1: true, p2: true, p3: true, p4: true}
	branches := map[string]bool{b1: true, b2: true, b3: true, b4: true}
	if len(paths) != 4 || len(branches) != 4 {
		t.Errorf("got %d paths, %d branches; want 4 of each", len(paths), len(branches))
	}
}

// Reusing a folder whose branch was switched would hand an agent the wrong
// work, so Create must refuse.
func TestCreateReuseWithWrongBranchFails(t *testing.T) {
	repo, root := newRepo(t), t.TempDir()
	p, _, err := Create(repo, root, "01", "x")
	if err != nil {
		t.Fatal(err)
	}
	run(t, p, "checkout", "-q", "-b", "other")
	if _, _, err := Create(repo, root, "01", "x"); err == nil {
		t.Error("got nil, want error for wrong branch")
	}
}

// A folder from a different repo must never be adopted.
func TestCreateRefusesFolderOfAnotherRepo(t *testing.T) {
	r1, r2, root := newRepo(t), newRepo(t), t.TempDir()
	if _, _, err := Create(r1, root, "01", "x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(r2, root, "01", "x"); err == nil {
		t.Error("got nil, want error")
	}
}

// Uncommitted work must survive a plain Remove; only Discard may delete it.
func TestRemoveRefusesDirtyTreeAndDiscardDeletesIt(t *testing.T) {
	repo, root := newRepo(t), t.TempDir()
	p, _, _ := Create(repo, root, "01", "gone")
	os.WriteFile(filepath.Join(p, "dirty.txt"), []byte("x"), 0o644)
	if err := Remove(repo, root, p); !errors.Is(err, ErrDirty) {
		t.Fatalf("Remove dirty: got %v, want ErrDirty", err)
	}
	if _, err := os.Stat(filepath.Join(p, "dirty.txt")); err != nil {
		t.Fatalf("dirty file lost: %v", err)
	}
	if err := Discard(repo, root, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("folder still exists, want removed")
	}
	if out := run(t, repo, "worktree", "list"); strings.Contains(out, "gone") {
		t.Errorf("worktree list still has it: %s", out)
	}
	if err := Remove(repo, root, p); err != nil {
		t.Errorf("second Remove: got %v, want nil", err)
	}
}

// A clean worktree is removed by plain Remove.
func TestRemoveDeletesCleanWorktree(t *testing.T) {
	repo, root := newRepo(t), t.TempDir()
	p, _, _ := Create(repo, root, "01", "clean")
	if err := Remove(repo, root, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("folder still exists, want removed")
	}
}

// A wrong path must never reach git's forced removal.
func TestRemoveAndDiscardRefusePathsOutsideRoot(t *testing.T) {
	repo, root := newRepo(t), t.TempDir()
	other := t.TempDir()
	for name, f := range map[string]func(string, string, string) error{"Remove": Remove, "Discard": Discard} {
		for _, p := range []string{other, root, repo, filepath.Join(root, "..")} {
			if err := f(repo, root, p); err == nil {
				t.Errorf("%s(%q): got nil, want refusal", name, p)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
		t.Errorf("repo was touched: %v", err)
	}
}

// Outside a repo the caller needs a clear error, not a git stack.
func TestCreateOutsideRepoFails(t *testing.T) {
	d := t.TempDir()
	if IsRepo(d) {
		t.Fatal("IsRepo(temp dir) = true, want false")
	}
	if _, _, err := Create(d, t.TempDir(), "01", "x"); err == nil {
		t.Error("got nil, want error")
	}
	if !IsRepo(newRepo(t)) {
		t.Error("IsRepo(repo) = false, want true")
	}
}
