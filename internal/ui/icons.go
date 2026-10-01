package ui

import (
	"embed"
	"fmt"
	"regexp"

	qt "github.com/mappu/miqt/qt6"
)

// The icons are Material Symbols, taken from Atlas Monitor's and Atlas Notes'
// sets (Apache 2.0, see NOTICE), plus Commander's own mark. Each is one colour,
// set at load: every fill in the file becomes the colour asked for.
//
//go:embed icons/*.svg
var iconFS embed.FS

var (
	svgFill = regexp.MustCompile(`fill="#[0-9a-fA-F]{3,8}"`)
	svgTag  = regexp.MustCompile(`<svg[^>]*>`)
	svgSize = regexp.MustCompile(`\s(width|height)="[^"]*"`)
)

type iconKey struct {
	name string
	c    rgb
	px   int
	dpr  float64
}

// iconCache holds rendered icons; a theme change asks for new colours, so it
// only grows by a handful per theme.
var iconCache = map[iconKey]*qt.QPixmap{}

// iconPixmap renders a named icon at px logical pixels in one colour, sharp at
// the given device pixel ratio. The pixmap is cached and must not be deleted.
func iconPixmap(name string, c rgb, alpha float64, px int, dpr float64) *qt.QPixmap {
	if dpr <= 0 {
		dpr = 1
	}
	c = rgb{c.r, c.g, c.b} // alpha goes in the fill, not the key's colour
	key := iconKey{name + fmt.Sprintf("@%.2f", alpha), c, px, dpr}
	if pm, ok := iconCache[key]; ok {
		return pm
	}
	data, err := iconFS.ReadFile("icons/" + name + ".svg")
	if err != nil {
		panic("ui: no icon " + name)
	}
	fill := fmt.Sprintf(`fill="%s" fill-opacity="%.3f"`, c.hex(), alpha)
	data = svgFill.ReplaceAll(data, []byte(fill))
	side := int(float64(px)*dpr + 0.5)
	data = svgTag.ReplaceAllFunc(data, func(tag []byte) []byte {
		tag = svgSize.ReplaceAll(tag, nil)
		return append([]byte(fmt.Sprintf(`<svg width="%d" height="%d"`, side, side)), tag[len("<svg"):]...)
	})
	pm := qt.NewQPixmap()
	if !pm.LoadFromData4(data, "SVG") {
		pm.Delete()
		pm = qt.NewQPixmap2(side, side)
		clear := qt.NewQColor2(qt.Transparent)
		pm.FillWithFillColor(clear)
		clear.Delete()
	}
	pm.SetDevicePixelRatio(dpr)
	iconCache[key] = pm
	return pm
}

// themedIcon is a QLabel showing an icon in the page's text colour, redrawn
// on every theme change.
type themedIcon struct {
	L     *qt.QLabel
	name  string
	px    int
	alpha float64
}

func newThemedIcon(a *App, name string, px int, alpha float64) *themedIcon {
	t := &themedIcon{L: qt.NewQLabel2(), name: name, px: px, alpha: alpha}
	t.L.SetFixedSize2(px, px)
	paint := func() {
		if a.pal == nil { // before the first theme; applyTheme calls back
			return
		}
		t.L.SetPixmap(iconPixmap(t.name, a.pal.fg, t.alpha, t.px, a.win.DevicePixelRatioF()))
	}
	paint()
	a.themed = append(a.themed, paint)
	return t
}

// buttonIcon is an icon for a push button in the text colour.
func (a *App) buttonIcon(btn *qt.QPushButton, name string) {
	set := func() {
		if a.pal == nil {
			return
		}
		icon := qt.NewQIcon2(iconPixmap(name, a.pal.fg, 0.9, 16, a.win.DevicePixelRatioF()))
		btn.SetIcon(icon)
		icon.Delete()
	}
	set()
	a.themed = append(a.themed, set)
}
