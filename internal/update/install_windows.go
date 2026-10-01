package update

import "errors"

// Updating on Windows.
//
// Commander cannot update itself here, and the reason is worth stating rather
// than treating it as a missing feature. The Linux path pulls the checkout and
// rebuilds, which needs make, a C++ compiler, Qt's development files and a POSIX
// shell; a machine that downloaded the zip has none of them. The other obvious
// route, writing the new build over this one, fails because Windows keeps an
// executable's file locked while it runs, so a process cannot replace its own
// exe. Doing it properly takes a helper that waits for Commander to exit and
// then swaps the files, which is an installer, a different piece of software.
//
// So the check works, reading VERSION from the channel over HTTPS like every
// non-checkout install, and what it offers is the releases page.

// CanSelfInstall is false: see above.
const CanSelfInstall = false

// PlatformWording replaces the package-manager explanation, which describes a
// machine this is not.
func PlatformWording() (heading, body string, ok bool) {
	return "Update from the website",
		"Atlas Commander can't replace itself on Windows: the file is locked while " +
			"it is running. Download the new version and unpack it over this one, " +
			"with Commander closed.", true
}

// Relaunch is never reached, since nothing here installs an update. It exists so
// the package has the same functions on both platforms.
func Relaunch(string) error {
	return errors.New("restarting after an update is not supported on Windows")
}

// FindTerminal is nil: no package manager owns a Windows install, so there is
// no update command to run.
func FindTerminal() *Terminal { return nil }

// Run is never reached; see FindTerminal.
func (t Terminal) Run(string) error {
	return errors.New("running an update command is not supported on Windows")
}
