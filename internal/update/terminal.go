package update

// Running a package manager's update command where the user can see it.
//
// Commander does not run sudo itself: a password prompt belongs to the package
// manager, in front of the person typing it. So for a packaged install the
// dialog offers to open a terminal on the command, which waits for a key at the
// end so the output can still be read.

// Terminal is a terminal emulator and how it takes a command to run.
type Terminal struct {
	cmd  string
	args []string // everything before the command itself
}

// Name is the emulator's command, for a tooltip.
func (t Terminal) Name() string { return t.cmd }

// argv is what the terminal is exec'd with. cmd must arrive as one argument
// and never be spliced into a string another shell parses: bash -c runs the
// fixed wrapper, which runs "$@", and cmd is the last of those arguments.
func (t Terminal) argv(cmd string) []string {
	const wrapper = `"$@"; status=$?; printf '\n'; read -rsn1 -p 'Press any key to close…'; exit $status`
	args := append([]string{}, t.args...)
	return append(args, "bash", "-c", wrapper, "atlas-update", "bash", "-lc", cmd)
}

// terminals are tried in order. x-terminal-emulator comes first because on a
// Debian-derived system it is whichever one the user chose; the rest are the
// defaults of the desktops Commander is likely to run on, then the popular
// standalone ones. Each takes the command as separate arguments after its
// flag; a terminal whose flag takes one string to re-split (tilix's -e) would
// run the wrapper as garbage, so it is not on the list.
var terminals = []Terminal{
	{"x-terminal-emulator", []string{"-e"}},
	{"konsole", []string{"-e"}},
	{"ptyxis", []string{"--"}},
	{"kgx", []string{"--"}},
	{"gnome-terminal", []string{"--"}},
	{"xfce4-terminal", []string{"-x"}},
	{"ghostty", []string{"-e"}},
	{"alacritty", []string{"-e"}},
	{"kitty", nil},
	{"foot", nil},
	{"wezterm", []string{"start", "--"}},
	{"xterm", []string{"-e"}},
}
