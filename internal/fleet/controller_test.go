//go:build !windows

package fleet_test

import (
	"atlas-commander/internal/fleet"
	"atlas-commander/internal/ui"
)

// The supervisor is what the UI drives; this fails to compile if a
// Controller method drifts. It lives in an external test package because ui
// imports fleet.
var _ ui.Controller = (*fleet.Supervisor)(nil)
