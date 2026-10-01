// Package procgroup runs an agent and everything it starts as one unit, so
// Commander can stop all of it at once and a Commander crash leaves nothing
// running behind.
//
// Linux puts the agent in its own process group; Windows assigns it to a Job
// Object created with kill-on-close. Both sit behind Group. The platform
// bodies live in procgroup_linux.go and procgroup_windows.go.
package procgroup

import "os/exec"

// InstanceEnv is set in every agent's environment to the id of the Commander
// instance that started it. ReapOrphans uses it to find processes left by an
// instance that crashed.
const InstanceEnv = "ATLAS_COMMANDER_INSTANCE"

// Group owns one started command and all of its descendants.
// A Group is safe for concurrent use.
type Group interface {
	// Start starts cmd as the root of the group. Call it instead of
	// cmd.Start; the caller still calls cmd.Wait.
	Start(cmd *exec.Cmd) error
	// Terminate asks every process in the group to exit (SIGTERM on Linux).
	// Windows has no polite equivalent, so there it is the same as Kill.
	Terminate() error
	// Kill ends every process in the group immediately.
	Kill() error
	// Close releases the group's handles. On Windows this also kills any
	// process still in the job.
	Close() error
}
