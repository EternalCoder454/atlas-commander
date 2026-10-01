package update

import (
	"strconv"
	"strings"
)

// Compare orders two versions: negative when a is older than b, zero when they
// are the same, positive when a is newer. A leading "v" is ignored.
//
// "0.2.0-beta" is older than "0.2.0": a beta is the run-up to its release, and
// a person on the beta channel should be offered the release when it ships.
// Two prereleases of the same number are ordered by their suffix.
func Compare(a, b string) int {
	ac, ap := splitVersion(a)
	bc, bp := splitVersion(b)
	if c := comparePart(ac, bc); c != 0 {
		return c
	}
	switch {
	case ap == "" && bp == "":
		return 0
	case ap == "":
		return 1 // a is the release, b its prerelease
	case bp == "":
		return -1
	}
	return comparePart(ap, bp)
}

// splitVersion separates "0.2.0-beta" into "0.2.0" and "beta".
func splitVersion(v string) (core, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, pre, _ = strings.Cut(v, "-")
	return core, pre
}

// comparePart compares dot-separated parts, numerically where both are numbers.
func comparePart(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		x, y := at(as, i), at(bs, i)
		nx, errX := strconv.Atoi(x)
		ny, errY := strconv.Atoi(y)
		switch {
		case errX == nil && errY == nil:
			if nx != ny {
				return nx - ny
			}
		case x != y:
			return strings.Compare(x, y)
		}
	}
	return 0
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "0" // "1.0" is "1.0.0"
}
