// Package atlascommander holds what the whole build shares: the version.
package atlascommander

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var version string

// Version is the release in VERSION, e.g. "0.1.0" or "0.2.0-beta".
func Version() string { return strings.TrimSpace(version) }
