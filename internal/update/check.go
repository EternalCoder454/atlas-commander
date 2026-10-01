package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Info is what a check found: whether the channel has something newer, which
// version that is, and what changed in words a person who does not build the
// app can read.
type Info struct {
	Available bool
	Version   string   // "0.2.0", empty when it could not be read
	Changes   []string // changelog bullets for that version
	// Summary is one line for a log or a tooltip, e.g. "Release is up to date (v0.1.0)".
	Summary string
	// Switch is set when what is on offer is another channel's build rather
	// than a newer one of this: the user chose it in Settings, and it is theirs
	// to apply there. The check at launch leaves it alone.
	Switch bool
}

// RawBase is where the remote check reads VERSION and CHANGELOG.md, followed by
// "/<branch>/". A variable so that tests can point it at httptest.
var RawBase = rawURL

const (
	fetchTimeout  = 12 * time.Second
	maxFetchBytes = 512 << 10
)

// Errors a check returns. Their text is shown to the user as it is.
var (
	ErrOffline   = errors.New("couldn't reach GitHub")
	ErrNoVersion = errors.New("couldn't read the latest version")
)

// Check reports what channel is offering to a copy running version local.
//
// How it finds out depends on how Commander was installed. A source checkout is
// compared commit by commit with git, which is exact and notices work that has
// not been given a version number yet. Anything else has no checkout to compare
// in and asks the channel for its VERSION file over HTTPS. Neither changes
// anything on disk.
func Check(in Install, channel, local string) (Info, error) {
	if in.Kind == FromSource && in.Source != "" {
		if _, err := git(in.Source, "rev-parse", "--git-dir"); err == nil {
			return checkGit(in.Source, channel, in.Commit)
		}
	}
	return checkRemote(channel, local)
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

func checkGit(src, channel, installed string) (Info, error) {
	var info Info
	branch, _ := git(src, "rev-parse", "--abbrev-ref", "HEAD")
	if !IsChannel(branch) {
		// Updating would have to switch branches and leave this one behind, so
		// the updater refuses; saying so here keeps the two in agreement.
		info.Summary = "Your checkout is on another branch"
		return info, nil
	}
	if !OfficialOrigin(originURL(src)) {
		// The updater refuses to build from any other origin (scripts/update.sh
		// checks the same list); offering an update it will refuse would only
		// end in a failed update.
		info.Summary = "Your checkout's origin isn't the Atlas Commander repository"
		return info, nil
	}
	if _, err := git(src, "fetch", "--quiet", "origin", channel); err != nil {
		return info, ErrOffline
	}
	local, _ := git(src, "rev-parse", "--short", "HEAD")
	remote, _ := git(src, "rev-parse", "--short", "origin/"+channel)
	label := ChannelLabel(channel)
	remoteVersion := func() string {
		v, _ := git(src, "show", "origin/"+channel+":VERSION")
		return v
	}

	// On another channel's branch, with nothing uncommitted: what is on offer
	// is the move, not a newer commit.
	dirty, _ := git(src, "status", "--porcelain")
	if branch != channel && IsChannel(branch) && dirty == "" {
		info.Available, info.Switch = true, true
		info.Version = remoteVersion()
		info.Summary = fmt.Sprintf("Switch to %s: %s → %s", label, local, remote)
		return info, nil
	}

	// What counts is what was built and installed, not what is checked out: if
	// a pull worked and the build after it failed, HEAD is new and the binary
	// is not. Without a record of the built commit, HEAD is the best guess.
	built := "HEAD"
	if installed != "" {
		// cat-file -t rather than the rev^{commit} syntax: MSYS2's git, run
		// from a Windows program, glob-expands braces in its arguments.
		if t, err := git(src, "cat-file", "-t", installed); err == nil && t == "commit" {
			built = installed
			local = installed
			if len(local) > 7 {
				local = local[:7]
			}
		}
	}
	if exec.Command("git", "-C", src, "merge-base", "--is-ancestor", "origin/"+channel, built).Run() == nil {
		info.Summary = fmt.Sprintf("Up to date on %s (%s)", label, local)
		return info, nil
	}
	info.Available = true
	info.Version = remoteVersion()
	info.Summary = fmt.Sprintf("%s update available: %s → %s", label, local, remote)
	if md, err := git(src, "show", "origin/"+channel+":CHANGELOG.md"); err == nil {
		info.Changes = Changelog(md, info.Version)
	}
	return info, nil
}

func checkRemote(channel, local string) (Info, error) {
	var info Info
	version, err := fetchText(RawBase + "/" + channel + "/VERSION")
	if err != nil {
		return info, ErrOffline
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return info, ErrNoVersion
	}
	label := ChannelLabel(channel)
	local = strings.TrimSpace(local)
	if Compare(version, local) <= 0 {
		info.Summary = fmt.Sprintf("Up to date on %s (v%s)", label, local)
		return info, nil
	}
	info.Available = true
	info.Version = version
	info.Summary = fmt.Sprintf("%s update available: v%s → v%s", label, local, version)
	if md, err := fetchText(RawBase + "/" + channel + "/CHANGELOG.md"); err == nil {
		info.Changes = Changelog(md, version)
	}
	return info, nil
}

func fetchText(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "atlas-commander")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	// Capped: a wrong URL must not be able to feed the app an endless body.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// originURL is the checkout's origin. A variable so tests, whose origin is a
// local bare repository, can stand in the real repository's URL.
var originURL = func(src string) string {
	u, _ := git(src, "remote", "get-url", "origin")
	return u
}

// OfficialOrigin says whether a git remote URL is the Atlas Commander
// repository on GitHub. It accepts the same forms scripts/update.sh does:
// HTTPS, ssh:// and scp-style, with or without ".git".
func OfficialOrigin(url string) bool {
	const path = "EternalCoder454/atlas-commander"
	for _, prefix := range []string{"https://github.com/", "ssh://git@github.com/", "git@github.com:"} {
		rest, ok := strings.CutPrefix(url, prefix)
		if !ok {
			continue
		}
		rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
		return rest == path
	}
	return false
}
