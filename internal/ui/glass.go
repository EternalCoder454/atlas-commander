package ui

import (
	"runtime"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/theme"
)

// transparencyPlatform is whether the window is made translucent-capable at
// all: Linux only. Windows layered windows and Qt's translucency do not mix
// well, so Windows keeps an opaque window and the setting is unavailable.
func transparencyPlatform() bool { return runtime.GOOS != "windows" }

// transparencyAvailable says whether window transparency can work here and,
// when it cannot, why in words the settings card shows as its subtitle.
// On Wayland a compositor is always there. On X11 Qt offers no way to ask, so
// a compositor is assumed; without one the translucent parts draw against
// black rather than breaking anything.
func transparencyAvailable() (bool, string) {
	if !transparencyPlatform() {
		return false, "Not available on Windows"
	}
	switch qt.QGuiApplication_PlatformName() {
	case "wayland", "wayland-egl", "xcb", "offscreen", "minimal":
		return true, ""
	}
	return false, "Needs a desktop that composites windows"
}

// effectiveGlass is the opacities to paint with: the chosen level where
// transparency is available, solid everywhere else.
func (a *App) effectiveGlass() theme.Glass {
	if ok, _ := transparencyAvailable(); !ok {
		return theme.GlassFor("off")
	}
	return theme.GlassFor(a.settings.Transparency)
}
