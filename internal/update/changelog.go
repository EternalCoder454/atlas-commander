package update

import "strings"

// maxChangelogBullets caps how much the update prompt shows. A release with
// thirty entries would fill the dialog and stop being readable, and the point
// of the list is to answer "is this worth doing now".
const maxChangelogBullets = 8

// Changelog returns the bullets under the "## <version>" heading of CHANGELOG.md,
// at most maxChangelogBullets, with indented continuation lines folded into the
// bullet above.
//
// The beta channel's VERSION reads "0.2.0-beta" but its changelog heading may
// be either that or plain "0.2.0", so both are accepted.
func Changelog(markdown, version string) []string {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	core, _ := splitVersion(version)
	var out []string
	inSection := false
	for _, raw := range strings.Split(markdown, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if strings.HasPrefix(line, "## ") {
			if inSection {
				break // the next version's section: we are done
			}
			h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			inSection = h == version || h == core
			continue
		}
		if !inSection {
			continue
		}
		switch {
		case strings.HasPrefix(line, "- "):
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
		case strings.HasPrefix(line, "  ") && len(out) > 0 && strings.TrimSpace(line) != "":
			out[len(out)-1] += " " + strings.TrimSpace(line)
		}
		if len(out) == maxChangelogBullets {
			break
		}
	}
	return out
}
