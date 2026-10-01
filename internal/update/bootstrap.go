package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"atlas-commander/internal/paths"
)

// Fetching the source for a copy of Commander that arrived without any.
//
// A release tarball installs prebuilt binaries and nothing else, so there is no
// checkout to pull. Cloning the repository once turns that copy into one that
// updates itself like any build from source. Bootstrap does not record the
// clone: make install does, with the commit it built, once the build has worked.
// Until then the copy is still Standalone and is checked by version, so a first
// build that fails is still offered again, and the next try reuses the clone.
//
// Rebuilding needs a toolchain, and a machine that installed a prebuilt tarball
// is exactly the machine that may not have one. That is checked before anything
// is downloaded, and what is missing is named along with the command that
// installs it, rather than left for the user to find as a page of compiler
// errors in the update log.

// SetupError is something the user has to install before an update can be
// built. It is not a failure of the update: nothing was changed and nothing is
// broken, so the dialog shows it as instructions rather than as an error with a
// log.
type SetupError struct{ msg string }

func (e *SetupError) Error() string { return e.msg }

// cloneURL is where Bootstrap clones from. A variable so tests can clone a
// local repository instead of GitHub.
var cloneURL = RepoURL + ".git"

// SourceDir is where Bootstrap keeps the source it fetched.
func SourceDir() string { return filepath.Join(paths.Data(), "src") }

// Bootstrap fetches the source for a Standalone install and returns the install
// as the source install Run can update. status is told what is happening, from
// this goroutine.
//
// It runs git and pkg-config and may take a while, so call it off the Qt
// thread.
func Bootstrap(in Install, channel string, status func(string)) (Install, error) {
	if missing := missingTools(); len(missing) > 0 {
		return in, missingToolsError(missing)
	}
	dir := SourceDir()
	// The folder is cleared before a clone, so it must be the data folder and
	// never a path relative to wherever Commander was started from, which is
	// what paths.Data gives without a home directory.
	if !filepath.IsAbs(dir) {
		return in, errors.New("couldn't find a folder to keep the source in")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		// A clone from an earlier try. If git can't read it, say so rather than
		// delete it: the failure may be git's (a safe.directory refusal, say),
		// not the clone's.
		if _, err := git(dir, "rev-parse", "--git-dir"); err != nil {
			return in, fmt.Errorf("git can't read the source in %s", dir)
		}
	} else {
		// Anything else in the way is a half-finished clone or not a checkout
		// at all, and cannot be pulled into one.
		if err := os.RemoveAll(dir); err != nil {
			return in, fmt.Errorf("couldn't clear %s: %w", dir, err)
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return in, err
		}
		status("Fetching the source… This happens once; later updates are quicker.")
		cmd := exec.Command("git", "clone", "--branch", channel, cloneURL, dir)
		// A clone that wants a password has gone wrong (the repository is
		// public), and git asking on a terminal nobody can see would hang.
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(dir) // a half-finished clone would be taken for a checkout
			return in, fmt.Errorf("couldn't download the source: %s", lastLine(string(out)))
		}
	}
	in.Kind, in.Source, in.Writable = FromSource, dir, false
	return in, nil
}

// buildTool is one thing a rebuild needs.
type buildTool struct {
	label string
	// alternatives are commands that satisfy it; any one will do. Empty for a
	// library, which pkg-config answers for instead.
	alternatives []string
	// pkgConfig is the library's pkg-config name, for a library.
	pkgConfig string
	// packages names it per package manager, for the install hint.
	packages map[string]string
}

// buildTools is what building Commander needs beyond the source, matching the
// list setup.sh checks. cgo is not optional: MIQT is C++ bindings to Qt.
var buildTools = []buildTool{
	{label: "git", alternatives: []string{"git"}, packages: map[string]string{
		"pacman": "git", "apt": "git", "dnf": "git", "zypper": "git", "yum": "git",
	}},
	{label: "Go", alternatives: []string{"go"}, packages: map[string]string{
		"pacman": "go", "apt": "golang-go", "dnf": "golang", "zypper": "go", "yum": "golang",
	}},
	{label: "a C++ compiler", alternatives: []string{"g++", "c++", "clang++"}, packages: map[string]string{
		"pacman": "base-devel", "apt": "build-essential", "dnf": "gcc-c++", "zypper": "gcc-c++", "yum": "gcc-c++",
	}},
	{label: "pkg-config", alternatives: []string{"pkg-config", "pkgconf"}, packages: map[string]string{
		"pacman": "pkgconf", "apt": "pkg-config", "dnf": "pkgconf-pkg-config", "zypper": "pkg-config", "yum": "pkgconfig",
	}},
	{label: "make", alternatives: []string{"make", "just"}, packages: map[string]string{
		"pacman": "base-devel", "apt": "make", "dnf": "make", "zypper": "make", "yum": "make",
	}},
	// A prebuilt tarball runs on the Qt runtime alone, so the development
	// files are routinely missing on exactly the installs that get here.
	{label: "the Qt 6 development files", pkgConfig: "Qt6Widgets", packages: map[string]string{
		"pacman": "qt6-base", "apt": "qt6-base-dev", "dnf": "qt6-qtbase-devel", "zypper": "qt6-base-devel", "yum": "qt6-qtbase-devel",
	}},
}

// missingTools is what this machine lacks for a build. A variable so tests can
// say what is installed.
var missingTools = func() []buildTool {
	return missingFrom(func(names ...string) bool { return which(names...) != "" }, pkgConfigHas)
}

// missingFrom is the check with the machine passed in: have reports whether
// any of the commands is on PATH, hasLib whether pkg-config knows a library.
func missingFrom(have func(...string) bool, hasLib func(string) bool) []buildTool {
	var missing []buildTool
	pkgConfig := have("pkg-config", "pkgconf")
	for _, t := range buildTools {
		switch {
		case t.pkgConfig == "":
			if !have(t.alternatives...) {
				missing = append(missing, t)
			}
		case pkgConfig:
			// Without pkg-config there is no asking about libraries; it is on
			// the list already and the hint names it.
			if !hasLib(t.pkgConfig) {
				missing = append(missing, t)
			}
		}
	}
	return missing
}

func pkgConfigHas(lib string) bool {
	tool := which("pkg-config", "pkgconf")
	return tool != "" && exec.Command(tool, "--exists", lib).Run() == nil
}

// missingToolsError says what is missing and how to get it, as one message the
// update dialog can show as it stands.
func missingToolsError(missing []buildTool) error {
	labels := make([]string, len(missing))
	for i, m := range missing {
		labels[i] = m.label
	}
	msg := "Commander builds the new version from source, and this system is missing " +
		joinWords(labels) + "."
	for _, m := range installers {
		if which(m.tool) != "" {
			if cmd := installHint(m.manager, missing); cmd != "" {
				msg += "\n\nInstall them with:\n" + cmd
			}
			break
		}
	}
	return &SetupError{msg}
}

// installers are the package managers that can install the build tools: the
// key buildTool.packages uses, the program that shows it is there, and the
// command that installs.
var installers = []struct{ manager, tool, cmd string }{
	{"pacman", "pacman", "sudo pacman -S --needed"},
	{"apt", "apt-get", "sudo apt install"},
	{"dnf", "dnf", "sudo dnf install"},
	{"zypper", "zypper", "sudo zypper install"},
	{"yum", "yum", "sudo yum install"},
}

// installHint is the command that installs the missing tools with one package
// manager, or "" when it is not one Commander knows. It takes the manager rather
// than looking for one, so each manager's wording can be checked on a machine
// that does not have it.
func installHint(manager string, missing []buildTool) string {
	var cmd string
	for _, m := range installers {
		if m.manager == manager {
			cmd = m.cmd
		}
	}
	if cmd == "" {
		return ""
	}
	var pkgs []string
	for _, t := range missing {
		if p := t.packages[manager]; p != "" && !contains(pkgs, p) {
			pkgs = append(pkgs, p)
		}
	}
	if len(pkgs) == 0 {
		return ""
	}
	return cmd + " " + strings.Join(pkgs, " ")
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// joinWords lists things the way a sentence does: "a, b and c".
func joinWords(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

// lastLine is the final non-blank line of command output, which is where git
// puts the reason it gave up.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "no output"
}
