// Package qtx holds the few Qt calls MIQT does not bind. MIQT v0.14 is
// generated against Qt 6.4, and QStyleHints::colorScheme arrived in 6.5, so
// following the system's light/dark choice needs this small C++ shim.
//
// Everything here must be called on the Qt main thread after the
// QApplication exists.
package qtx

/*
#cgo CXXFLAGS: -std=c++17
#cgo pkg-config: Qt6Gui
#include "colorscheme.h"
*/
import "C"

// Scheme is the desktop's colour scheme as Qt reports it.
type Scheme int

const (
	SchemeUnknown Scheme = 0
	SchemeLight   Scheme = 1
	SchemeDark    Scheme = 2
)

// SystemScheme reports the desktop's light/dark preference. Unknown means
// the platform does not say (some X11 desktops); callers then fall back to
// the palette's lightness.
func SystemScheme() Scheme { return Scheme(C.atlas_color_scheme()) }
