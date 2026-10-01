// Package observed reads Claude Code sessions that were started outside
// Commander, from the transcripts Claude Code writes under
// ~/.claude/projects. It only ever reads: no process sniffing, no writes,
// no locks on the files. Every function is synchronous and safe for
// concurrent use (no shared state); callers run them off the UI thread. The
// code is platform neutral.
package observed

import "time"

// Session is one transcript file.
type Session struct {
	Path      string
	ID        string // session id (the file name without .jsonl)
	Project   string // the working directory the session ran in
	Title     string // the first user prompt, one line
	Model     string // the last model seen
	GitBranch string
	Started   time.Time
	Modified  time.Time
	Size      int64
	Messages  int  // user + assistant messages
	Live      bool // written in the last two minutes
}

// Line is one transcript entry as the observed view shows it.
type Line struct {
	At      time.Time
	Role    string // "user", "assistant", "tool", "result", "system"
	Text    string
	Tool    string
	IsError bool
}
