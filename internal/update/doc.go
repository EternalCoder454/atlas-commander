// Package update is Commander's update system: how this copy was installed,
// whether its channel has something newer, what changed, and how to apply it.
//
// It has no Qt in it, so it builds and is tested without a display, and the UI
// only formats what it returns.
//
// Goroutine model: nothing here keeps state. Every function is safe to call
// from any goroutine and blocks until it has an answer (the check does network
// or git work, the run waits for scripts/update.sh), so callers run them off
// the Qt thread and hand the result back through their own mutex-guarded state.
// The only package variable, RawBase, is set by tests before they start any
// goroutine.
//
// Platform split: the shared files hold the types, version and changelog logic
// and the check. install_unix.go and install_windows.go carry the same three
// functions each (CanSelfInstall, PlatformWording, Relaunch). Windows cannot
// replace a running executable and has no make or bash on a typical machine, so
// there the check still works and the offer is the releases page.
package update
