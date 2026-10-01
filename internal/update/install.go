package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"atlas-commander/internal/paths"
)

// Where Commander lives online. Channels are branches of this repository.
const (
	RepoURL = "https://github.com/EternalCoder454/atlas-commander"
	rawURL  = "https://raw.githubusercontent.com/EternalCoder454/atlas-commander"
)

// Channels Commander follows. There is no main branch and no minimal build.
const (
	ChannelRelease = "release"
	ChannelBeta    = "beta"
)

// ChannelLabel is the name a person sees for a channel.
func ChannelLabel(channel string) string {
	if channel == ChannelBeta {
		return "Beta"
	}
	return "Release"
}

// IsChannel reports whether a git branch name is one of the channels.
func IsChannel(branch string) bool {
	return branch == ChannelRelease || branch == ChannelBeta
}

// binaryName is the package and executable name, and the fallback when a
// package manager reports a name that is not safe to put in a command.
const binaryName = "atlas-commander"

// Kind is how this copy of Commander got onto the machine, which decides how it
// can be updated.
type Kind int

const (
	// FromSource was built from a checkout by make install or just install,
	// which recorded where the checkout is. Updating pulls and rebuilds it.
	FromSource Kind = iota
	// FromPackage belongs to a package manager. Replacing its files from here
	// would leave the package database describing files that are gone, so the
	// package manager is the one that updates it.
	FromPackage
	// Standalone is anything else: an unpacked release tarball or zip. There is
	// no checkout to pull and nothing to rebuild from.
	Standalone
)

// Install describes this copy.
type Install struct {
	Kind Kind
	// Source is the checkout, for FromSource.
	Source string
	// Binary is the running executable with symlinks resolved; "" if unknown.
	Binary string
	// Manager and Package name the owner, for FromPackage: "pacman",
	// "apt", "dnf", "zypper", "yum" or "rpm".
	Manager string
	Package string
}

// Prefix is the install prefix the running copy lives under (the directory
// above bin), so a rebuild replaces it instead of landing somewhere else and
// leaving two Commanders for PATH to choose between. It is "" when unknown, and
// when the binary runs from the checkout's own bin/ (a development run), where
// installing "over" it would put files into the source tree.
func (in Install) Prefix() string {
	if in.Binary == "" {
		return ""
	}
	if in.Source != "" && within(in.Source, in.Binary) {
		return ""
	}
	return filepath.Dir(filepath.Dir(in.Binary))
}

// within reports whether path is inside dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// SelfUpdatable is whether Commander can apply an update itself.
func (in Install) SelfUpdatable() bool { return CanSelfInstall && in.Kind == FromSource }

// UpdateCommand is the command that updates a packaged install, or "".
func (in Install) UpdateCommand() string {
	if in.Kind != FromPackage {
		return ""
	}
	return Command(in.Manager, in.Package, which(aurHelpers...))
}

// Command builds the update command for one package manager. A package name
// that is not plain characters is replaced by Commander's own, because the
// result is shown to be pasted into a terminal.
func Command(manager, pkg, aurHelper string) string {
	if !safePackageName(pkg) {
		pkg = binaryName
	}
	switch manager {
	case "pacman":
		if aurHelper != "" {
			return aurHelper + " -Syu " + pkg
		}
		return "git clone " + RepoURL + ".git && cd atlas-commander/packaging && makepkg -si"
	case "apt":
		return "sudo apt update && sudo apt install --only-upgrade " + pkg
	case "dnf":
		return "sudo dnf upgrade " + pkg
	case "yum":
		return "sudo yum update " + pkg
	case "zypper":
		return "sudo zypper update " + pkg
	case "":
		return ""
	default:
		return manager + " " + pkg
	}
}

func safePackageName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '+':
		default:
			return false
		}
	}
	return true
}

// aurHelpers are tried in order for an Arch install built from the AUR.
var aurHelpers = []string{"paru", "yay", "pikaur", "trizen", "aura"}

// Wording is the heading and explanation for an install that is not updated by
// Commander itself.
func Wording(in Install) (heading, body string) {
	if h, b, ok := PlatformWording(); ok {
		return h, b
	}
	switch in.Kind {
	case FromPackage:
		mgr := in.Manager
		if mgr == "" {
			mgr = "your package manager"
		}
		return "Update with " + mgr,
			"Atlas Commander was installed by " + mgr + ", so " + mgr + " updates it too. " +
				"Installing over it from here would leave the package database describing " +
				"a file that is no longer there."
	}
	return "Download the new version",
		"This copy was not built from a checkout, so it can't rebuild itself. " +
			"Download the new version and unpack it over this one, with Commander closed."
}

// DownloadPage is where a new build for a channel is.
func DownloadPage(channel string) string {
	if channel == ChannelBeta {
		return RepoURL + "/releases"
	}
	return RepoURL + "/releases/latest"
}

// SourceFile is where make install and just install record the checkout. The
// recipes write ${XDG_DATA_HOME:-$HOME/.local/share}/atlas-commander/source;
// paths.Data is the same place, and also honours ATLAS_DATA_HOME so a sandboxed
// run reads its own copy.
func SourceFile() string { return filepath.Join(paths.Data(), "source") }

func sourceDir(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// looksLikeCheckout guards against a stale record: the checkout may have been
// moved or deleted since the install.
func looksLikeCheckout(dir string) bool {
	for _, marker := range []string{filepath.Join("scripts", "update.sh"), "Makefile", "justfile", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// Detect works out how the running copy was installed. It runs package-manager
// queries, which can take a moment, so call it off the Qt thread.
func Detect() Install {
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return detect(SourceFile(), exe, packageOwner)
}

// detect is Detect with its inputs passed in, so tests can use temp
// directories and a fake package manager.
func detect(sourceFile, exe string, owner func(path string) (manager, pkg string, ok bool)) Install {
	in := Install{Binary: exe}
	// A recorded checkout wins over a package owner: a developer who ran make
	// install over a distro package has the checkout as the thing to update.
	if src := sourceDir(sourceFile); src != "" && looksLikeCheckout(src) {
		in.Kind, in.Source = FromSource, src
		return in
	}
	if exe != "" {
		if mgr, pkg, ok := owner(exe); ok {
			in.Kind, in.Manager, in.Package = FromPackage, mgr, pkg
			return in
		}
	}
	in.Kind = Standalone
	return in
}

// packageOwner asks each package manager that is installed whether it owns
// path, the first yes wins.
func packageOwner(path string) (manager, pkg string, ok bool) {
	for _, q := range ownerQueries {
		if which(q.tool) == "" {
			continue
		}
		out, err := exec.Command(q.tool, append(q.args, path)...).Output()
		if err != nil {
			continue // not owned by this one, or it could not say
		}
		name := q.name(strings.TrimSpace(string(out)))
		if name == "" {
			continue
		}
		return q.updater(), name, true
	}
	return "", "", false
}

// ownerQueries are the package managers' "which package owns this file?"
// commands, with a parser for each one's output.
var ownerQueries = []struct {
	tool    string
	args    []string
	name    func(output string) string
	updater func() string
}{
	{"pacman", []string{"-Qo"}, parsePacman, func() string { return "pacman" }},
	{"dpkg-query", []string{"-S"}, parseDpkg, func() string { return "apt" }},
	{"rpm", []string{"-qf", "--queryformat", "%{NAME}"}, firstField, rpmFrontEnd},
}

// parsePacman reads "/usr/bin/atlas-commander is owned by atlas-commander 0.1.0-1".
func parsePacman(s string) string {
	_, after, found := strings.Cut(s, " is owned by ")
	if !found {
		return ""
	}
	return firstField(after)
}

// parseDpkg reads "atlas-commander: /usr/bin/atlas-commander".
func parseDpkg(s string) string {
	before, _, found := strings.Cut(s, ":")
	if !found {
		return ""
	}
	return firstField(before)
}

// rpmFrontEnd is the tool that updates an rpm: dnf, zypper or yum, whichever
// is installed, and plain rpm if none is.
func rpmFrontEnd() string {
	for _, m := range []string{"dnf", "zypper", "yum"} {
		if which(m) != "" {
			return m
		}
	}
	return "rpm"
}

func firstField(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// which returns the first of names found on PATH, or "".
func which(names ...string) string {
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return n
		}
	}
	return ""
}
