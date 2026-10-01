package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
)

// Tokens are the colours the custom-painted widgets need, as #rrggbb. They
// come from the active theme (see tokensFromTheme); QSS covers the stock
// widgets, but the board, sidebar and charts paint themselves and read these.
type Tokens struct {
	Dark bool

	Frame   string // window background: title area + sidebar (sidebar_bg)
	Page    string // the rounded sheet the views sit on (window_bg)
	View    string // tables and text areas (view_bg)
	Card    string
	Fg      string // body text
	Accent  string // accent as text and the active-row bar (accent_color)
	AccentB string // accent as a fill (accent_bg_color)
	AccentF string // text on an accent fill

	OK, Warn, Error, Idle string

	Charts [5]string
}

// rgb is a parsed colour. Painting code works in rgb and only makes a QColor
// at the last moment, because every QColor made from Go must be deleted.
type rgb struct{ r, g, b float64 }

func parseHex(s string) rgb {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return rgb{}
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return rgb{}
	}
	return rgb{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff)}
}

// mix returns a blended toward b by t (0..1), as Monitor derives its
// hairlines: a solid colour, so overlapping lines never darken each other.
func (a rgb) mix(b rgb, t float64) rgb {
	return rgb{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t}
}

func (a rgb) scale(k float64) rgb { return rgb{a.r * k, a.g * k, a.b * k} }

// hex is the colour as #rrggbb.
func (a rgb) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", clamp255(a.r), clamp255(a.g), clamp255(a.b))
}

func clamp255(v float64) int { return int(math.Max(0, math.Min(255, math.Round(v)))) }

// q makes a QColor with alpha a (0..1). The caller owns it and must Delete it.
func (a rgb) q(alpha float64) *qt.QColor {
	return qt.NewQColor11(clamp255(a.r), clamp255(a.g), clamp255(a.b), clamp255(alpha*255))
}

// palette is Tokens parsed once per theme change, with the derived shades the
// painters use. Monitor's values: hover 5%, selected 8%, page hairline 7%,
// grid 8%, plot border 30% of the text colour.
type palette struct {
	t Tokens

	frame, page, view, fg, accent, accentB, accentF rgb
	ok, warn, err, idle                             rgb
	charts                                          [5]rgb

	divider, rowline rgb // solid table lines, blended from page toward fg
}

func newPalette(t Tokens) *palette {
	p := &palette{
		t:       t,
		frame:   parseHex(t.Frame),
		page:    parseHex(t.Page),
		view:    parseHex(t.View),
		fg:      parseHex(t.Fg),
		accent:  parseHex(t.Accent),
		accentB: parseHex(t.AccentB),
		accentF: parseHex(t.AccentF),
		ok:      parseHex(t.OK),
		warn:    parseHex(t.Warn),
		err:     parseHex(t.Error),
		idle:    parseHex(t.Idle),
	}
	for i, c := range t.Charts {
		p.charts[i] = parseHex(c)
	}
	// Monitor computes these by contrast target; the fixed blends below land
	// on the same values for its dark (#414144 / #313135) and light
	// (#dfdfe0 / #eeeeef) schemes closely enough to be indistinguishable.
	if t.Dark {
		p.divider = p.page.mix(p.fg, 0.12)
		p.rowline = p.page.mix(p.fg, 0.06)
	} else {
		p.divider = p.page.mix(p.fg, 0.11)
		p.rowline = p.page.mix(p.fg, 0.05)
	}
	return p
}

// chartLine is the colour a series line is drawn in. On light themes Monitor
// darkens lines by 0.72 per channel so they hold against white; the wash
// fill keeps the original colour.
func (p *palette) chartLine(i int) rgb {
	c := p.charts[i%len(p.charts)]
	if !p.t.Dark {
		return c.scale(0.72)
	}
	return c
}

// defaultTokens is libadwaita 1.9's dark scheme as Atlas Monitor draws it,
// used until a theme is loaded.
func defaultTokens() Tokens {
	return Tokens{
		Dark:    true,
		Frame:   "#2e2e32",
		Page:    "#222226",
		View:    "#1d1d20",
		Card:    "#2e2e32",
		Fg:      "#ffffff",
		Accent:  "#81d1ff",
		AccentB: "#3584e4",
		AccentF: "#ffffff",
		OK:      "#78e9ab",
		Warn:    "#ffc252",
		Error:   "#ff938c",
		Idle:    "#646467",
		Charts:  [5]string{"#39b8e3", "#5c9efa", "#de68f2", "#f5628e", "#84c718"},
	}
}

// toneColor is the status colour for a tone.
func (p *palette) toneColor(t fleet.Tone) rgb {
	switch t {
	case fleet.ToneOK:
		return p.ok
	case fleet.ToneWarn:
		return p.warn
	case fleet.ToneError:
		return p.err
	}
	return p.idle
}
