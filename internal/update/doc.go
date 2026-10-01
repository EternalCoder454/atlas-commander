// Package update is Commander's update system: how this copy was installed,
// whether its channel has something newer, what changed, and how to apply it.
//
// It has no Qt in it, so it builds and is tested without a display, and the UI
// only formats what it returns.
//
// Goroutine model: nothing here keeps state in memory. Every function is safe to call
// from any goroutine and blocks until it has an answer (the check does network
// or git work, the run waits for scripts/update.sh), so callers run them off
// the Qt thread and hand the result back through their own mutex-guarded state.
// The package variables (RawBase, originURL, cloneURL, missingTools) are only
// replaced by tests, before they start any goroutine.
//
// Platform split: the shared files hold the types, version and changelog logic
// and the check. install_unix.go and install_windows.go carry the same
// functions each (CanSelfInstall, PlatformWording, Relaunch, FindTerminal and
// Terminal.Run). Windows cannot replace a running executable and has no make or
// bash on a typical machine, so there the check still works and the offer is
// the releases page.
//
// Bootstrap writes to disk: it clones the source for a standalone install into
// the data folder. Only the update dialog runs it, one at a time.
package update
