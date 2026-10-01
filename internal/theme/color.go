package theme

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// rgb is an sRGB colour with channels 0..255 as floats, so mixing does not
// round at every step.
type rgb struct{ r, g, b float64 }

// isHex reports whether s is a lower-case #rrggbb.
func isHex(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// parseHex reads #rrggbb in either case.
func parseHex(s string) (rgb, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !isHex(s) {
		return rgb{}, fmt.Errorf("%q is not a #rrggbb colour", s)
	}
	v, _ := strconv.ParseUint(s[1:], 16, 32)
	return rgb{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff)}, nil
}

// mustHex is for values that were validated or are literals in this package;
// bad input gives black rather than a panic in the UI.
func mustHex(s string) rgb {
	c, _ := parseHex(s)
	return c
}

func clamp255(v float64) float64 { return math.Min(255, math.Max(0, v)) }

func (c rgb) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", int(math.Round(clamp255(c.r))), int(math.Round(clamp255(c.g))), int(math.Round(clamp255(c.b))))
}

// over returns fg drawn over bg at the given alpha.
func over(fg, bg rgb, alpha float64) rgb {
	return rgb{
		fg.r*alpha + bg.r*(1-alpha),
		fg.g*alpha + bg.g*(1-alpha),
		fg.b*alpha + bg.b*(1-alpha),
	}
}

func (c rgb) scale(f float64) rgb { return rgb{c.r * f, c.g * f, c.b * f} }

// rgba renders a CSS/Qt rgba() with alpha trimmed of trailing zeros.
func (c rgb) rgba(alpha float64) string {
	a := strconv.FormatFloat(alpha, 'f', 3, 64)
	a = strings.TrimRight(strings.TrimRight(a, "0"), ".")
	if a == "" {
		a = "0"
	}
	return fmt.Sprintf("rgba(%d, %d, %d, %s)", int(math.Round(clamp255(c.r))), int(math.Round(clamp255(c.g))), int(math.Round(clamp255(c.b))), a)
}

func lin(v float64) float64 {
	v /= 255
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// luminance is the WCAG relative luminance, 0 (black) to 1 (white).
func (c rgb) luminance() float64 {
	return 0.2126*lin(c.r) + 0.7152*lin(c.g) + 0.0722*lin(c.b)
}

// contrast is the WCAG contrast ratio, 1 to 21.
func contrast(a, b rgb) float64 {
	la, lb := a.luminance(), b.luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
