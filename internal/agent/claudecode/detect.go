package claudecode

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Setup is what the setup screen shows about the Claude Code install.
type Setup struct {
	Path    string
	Version string
	// Problems are plain sentences, empty when Claude Code is ready.
	Problems []string
}

const versionTimeout = 10 * time.Second

// Detect finds the claude binary ("" = on PATH) and checks it runs.
func Detect(binary string) Setup {
	return detect(binary, runtime.GOOS)
}

func detect(binary, goos string) Setup {
	var s Setup
	path, err := resolve(binary)
	if err != nil {
		s.Problems = append(s.Problems,
			"Claude Code was not found. Install it with \"npm install -g @anthropic-ai/claude-code\" or with the official installer from claude.com/claude-code, then check again.")
	} else {
		s.Path = path
		ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "--version")
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		if err != nil {
			s.Problems = append(s.Problems, "Claude Code was found at "+path+" but \"claude --version\" failed. Reinstall it and try again.")
		} else {
			s.Version = strings.TrimSpace(string(out))
		}
	}
	if goos == "windows" && !hasGitBash() {
		s.Problems = append(s.Problems, "Git for Windows was not found. Claude Code needs Git Bash on Windows; install Git for Windows from git-scm.com.")
	}
	return s
}

func resolve(binary string) (string, error) {
	if binary == "" {
		return exec.LookPath("claude")
	}
	return exec.LookPath(binary)
}

func hasGitBash() bool {
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	_, err := exec.LookPath("git")
	return err == nil
}
