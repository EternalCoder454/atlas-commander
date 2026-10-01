// Package worktree gives each coding agent its own git worktree so agents
// working in the same repository don't overwrite each other's files.
//
// It shells out to the git binary, always with an argument list and never
// through a shell. Functions are synchronous and safe to call from any
// goroutine; Create and Remove serialise per repository themselves, since git
// itself locks only per operation. On Windows long paths are enabled for
// every git call; the short worktree root comes from internal/paths.
package worktree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// repoLocks holds one mutex per repository (keyed by its common git dir), so
// two agents starting at once don't race inside one repository's metadata.
var repoLocks sync.Map

func lockRepo(key string) func() {
	m, _ := repoLocks.LoadOrStore(key, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// repoKey identifies the repository containing dir; a dir that isn't one
// falls back to its own path, which still serialises calls on it.
func repoKey(dir string) string {
	if c, err := commonDir(dir); err == nil {
		return c
	}
	return filepath.Clean(dir)
}

// git runs git in dir and returns trimmed stdout. On failure the error holds
// git's own message, which is usually the useful part.
func git(dir string, args ...string) (string, error) {
	full := []string{}
	if runtime.GOOS == "windows" {
		full = append(full, "-c", "core.longpaths=true")
	}
	full = append(full, "-C", dir)
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(dir string) bool {
	out, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

func sanitize(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxNameLen {
		s = strings.Trim(s[:maxNameLen], "-")
	}
	if s == "" {
		s = "agent"
	}
	return s
}

func commonDir(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(out), nil
}

// maxNameLen keeps the folder name short: the worktree root sits under a
// user-level path on Windows, where total path length is limited.
const maxNameLen = 20

// ErrDirty means the worktree has changes that are not committed, so Remove
// refused to delete it.
var ErrDirty = errors.New("This worktree has changes that are not committed.")

// Create makes (or reuses) a worktree for the agent under root, on branch
// atlas/<name>-<id>, in folder <name>-<id>, and returns its path and branch.
// id is the agent's unique id, which keeps agents whose names fold to the
// same text apart. An existing branch is reused; an existing folder is reused
// only if it is a worktree of the same repository on the expected branch.
func Create(repoDir, root, id, name string) (path, branch string, err error) {
	top, err := git(repoDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", errors.New("This folder isn't inside a git repository.")
	}
	common, err := commonDir(top)
	if err != nil {
		return "", "", err
	}
	defer lockRepo(common)()
	id = sanitize(id)
	dir := sanitize(name) + "-" + id
	branch = "atlas/" + dir
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("Can't use %s.", root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", fmt.Errorf("Can't create %s.", root)
	}
	path = filepath.Join(root, dir)

	if _, statErr := os.Stat(path); statErr == nil {
		c, err := commonDir(path)
		if err != nil || c != common {
			return "", "", fmt.Errorf("The folder %s is already in use.", path)
		}
		if head, err := git(path, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || head != branch {
			return "", "", fmt.Errorf("The folder %s is on a different branch than %s.", path, branch)
		}
		return path, branch, nil // already ours
	}
	if _, err := git(top, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err = git(top, "worktree", "add", path, branch)
	} else {
		_, err = git(top, "worktree", "add", "-b", branch, path)
	}
	if err != nil {
		return "", "", fmt.Errorf("Couldn't create the worktree: %w", err)
	}
	return path, branch, nil
}

// resolve makes p absolute and follows symlinks as far as the path exists, so
// a link or ".." can't make a path outside root look inside it.
func resolve(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, nil
	}
	// The folder may be gone already; resolve its parent instead.
	parent, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return p, nil
	}
	return filepath.Join(parent, filepath.Base(p)), nil
}

// checkInside returns the resolved path if it is strictly inside root.
func checkInside(root, path string) (string, error) {
	r, err := resolve(root)
	if err != nil {
		return "", errors.New("Can't tell where the worktree folder is.")
	}
	p, err := resolve(path)
	if err != nil {
		return "", errors.New("Can't tell where the worktree folder is.")
	}
	rel, err := filepath.Rel(r, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("That folder isn't one of Commander's worktrees.")
	}
	return p, nil
}

// Remove deletes the worktree at path and prunes git's record of it. It
// refuses with ErrDirty if the worktree has uncommitted changes, and refuses
// any path outside root. The branch is kept so the work isn't lost.
func Remove(repoDir, root, path string) error {
	return remove(repoDir, root, path, false)
}

// Discard is Remove without the safety: it deletes the worktree even with
// uncommitted changes. Call it only after the user has confirmed.
func Discard(repoDir, root, path string) error {
	return remove(repoDir, root, path, true)
}

func remove(repoDir, root, path string, force bool) error {
	defer lockRepo(repoKey(repoDir))()
	p, err := checkInside(root, path)
	if err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	if _, err := git(repoDir, append(args, p)...); err != nil {
		// Folder already gone is fine: prune below cleans the record.
		if _, statErr := os.Stat(p); statErr == nil {
			if !force && strings.Contains(err.Error(), "--force") {
				return ErrDirty
			}
			return fmt.Errorf("Couldn't remove the worktree: %w", err)
		}
	}
	if _, err := git(repoDir, "worktree", "prune"); err != nil {
		return fmt.Errorf("Couldn't clean up worktree records: %w", err)
	}
	return nil
}

// Existing reports whether path is a worktree of the repository containing
// repoDir, on the given branch. The supervisor reuses such a folder, so an
// agent renamed since its worktree was made keeps working in the same one.
func Existing(repoDir, path, branch string) bool {
	if path == "" || branch == "" {
		return false
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return false
	}
	top, err := git(repoDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	want, err := commonDir(top)
	if err != nil {
		return false
	}
	got, err := commonDir(path)
	if err != nil || got != want {
		return false
	}
	head, err := git(path, "rev-parse", "--abbrev-ref", "HEAD")
	return err == nil && head == branch
}

// RemoveBranch deletes an atlas/ branch if it is fully merged. An unmerged
// branch is kept, since it holds work the user may want.
func RemoveBranch(repoDir, branch string) error {
	if !strings.HasPrefix(branch, "atlas/") {
		return fmt.Errorf("Won't delete the branch %s.", branch)
	}
	defer lockRepo(repoKey(repoDir))()
	if _, err := git(repoDir, "branch", "-d", branch); err != nil {
		return fmt.Errorf("Couldn't delete the branch %s: %w", branch, err)
	}
	return nil
}
