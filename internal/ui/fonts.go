package ui

import (
	"embed"
	"io/fs"
	"slices"

	qt "github.com/mappu/miqt/qt6"
)

// Bundled fonts are embedded rather than compiled with miqt-rcc: the effect
// is the same (no files to ship, identical on Linux and Windows) without a
// generator step in the build. The directory may hold only a README until
// the font files are added; then the system monospace font is used.
//
//go:embed fonts
var fontFS embed.FS

// loadFonts registers the bundled fonts and returns the monospace family to
// use: want if it is installed or bundled, else the system's fixed font.
func loadFonts(want string) string {
	files, _ := fs.Glob(fontFS, "fonts/*.ttf")
	for _, f := range files {
		data, err := fontFS.ReadFile(f)
		if err == nil {
			qt.QFontDatabase_AddApplicationFontFromData(data)
		}
	}
	if want != "" && slices.Contains(qt.QFontDatabase_Families(), want) {
		return want
	}
	return qt.QFontDatabase_SystemFont(qt.QFontDatabase__FixedFont).Family()
}
