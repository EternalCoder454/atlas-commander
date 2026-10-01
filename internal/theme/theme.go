// Package theme holds Commander's colour themes and the pure-Go half of
// applying them: the data model, JSON files, validation, and the Qt style
// sheet and palette strings. The Qt code that installs them lives in
// internal/ui, so this package imports no Qt and can be tested headless.
//
// The ten built-in palettes are the same ones Atlas Notes and Atlas Monitor
// ship, so the apps can be set to match. They use libadwaita's colour names
// because that is the family's shared palette model.
//
// Goroutines: Theme values are plain data; the built-in list is parsed once
// and every caller gets its own copy. Platform: none.
package theme

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"atlas-commander/internal/atomicfile"
	"atlas-commander/internal/paths"
)

//go:embed builtin/*.json
var builtinFS embed.FS

// Density is how tall rows are.
type Density string

// Densities, matching config.DensityChoices.
const (
	Comfortable Density = "comfortable"
	Compact     Density = "compact"
)

// Metrics are the pixel sizes the style sheet uses. They come from Atlas
// Notes and Monitor (libadwaita 1.9's 7px cards, 10px page corner, 5px rows).
type Metrics struct {
	RowHeight   int // table and list rows
	Radius      int // cards
	PageRadius  int // the page's rounded corner
	SmallRadius int // buttons, inputs, sidebar rows
	Hairline    int
	Pad         int // standard padding inside controls
}

// MetricsFor returns the sizes for d; anything unknown is comfortable.
func MetricsFor(d Density) Metrics {
	m := Metrics{RowHeight: 44, Radius: 7, PageRadius: 10, SmallRadius: 5, Hairline: 1, Pad: 8}
	if d == Compact {
		m.RowHeight = 40
		m.Pad = 6
	}
	return m
}

// BaseKeys are the 18 libadwaita-style colours every theme must define.
var BaseKeys = []string{
	"window_bg_color", "window_fg_color",
	"view_bg_color", "view_fg_color",
	"sidebar_bg_color", "sidebar_fg_color",
	"headerbar_bg_color", "headerbar_fg_color",
	"card_bg_color", "card_fg_color",
	"dialog_bg_color", "dialog_fg_color",
	"popover_bg_color", "popover_fg_color",
	"accent_bg_color", "accent_fg_color", "accent_color",
	"warning_color", "error_color", "success_color",
}

// DerivedKeys are Commander-only colours. A theme file may set them; when
// absent they are computed from the base keys.
var DerivedKeys = []string{
	"border_color", "grid_color",
	"status_ok", "status_warn", "status_error", "status_idle",
	"chart_1", "chart_2", "chart_3", "chart_4", "chart_5",
}

// chartBase are Atlas Monitor's series colours (cyan, blue, magenta, pink, lime).
var chartBase = [5]string{"#39b8e3", "#5c9efa", "#de68f2", "#f5628e", "#84c718"}

// Theme is one colour scheme. ID is stored in settings and never renamed.
type Theme struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Summary   string            `json:"summary"`
	Dark      bool              `json:"dark"` // not cosmetic: widgets and icons are drawn for the scheme
	Primary   string            `json:"primary"`
	Secondary string            `json:"secondary"` // equals accent_bg_color
	Colors    map[string]string `json:"colors"`

	// Density is set by WithOverrides; it is a setting, not part of a theme file.
	Density Density `json:"-"`
}

// clone returns a deep copy so callers can edit Colors freely.
func (t Theme) clone() Theme {
	c := make(map[string]string, len(t.Colors))
	for k, v := range t.Colors {
		c[k] = v
	}
	t.Colors = c
	return t
}

// Color returns the named colour, or "" if the theme has none.
func (t Theme) Color(key string) string { return t.Colors[key] }

// Filled returns a copy with every missing derived key computed from the base
// keys, so the UI never needs a fallback. Keys the theme sets are kept.
func (t Theme) Filled() Theme {
	t = t.clone()
	c := t.Colors
	bg, fg := mustHex(c["window_bg_color"]), mustHex(c["window_fg_color"])
	set := func(k, v string) {
		if _, ok := c[k]; !ok {
			c[k] = v
		}
	}
	// Alphas follow Monitor: content border fg@7-12%, grid fg@8%.
	set("border_color", over(fg, bg, 0.12).hex())
	set("grid_color", over(fg, bg, 0.08).hex())
	set("status_ok", c["success_color"])
	set("status_warn", c["warning_color"])
	set("status_error", c["error_color"])
	set("status_idle", over(fg, bg, 0.35).hex())
	for i, base := range chartBase {
		col := mustHex(base)
		if !t.Dark {
			// Monitor darkens the series on light themes so they read on a pale page.
			col = col.scale(0.72)
		}
		set(fmt.Sprintf("chart_%d", i+1), col.hex())
	}
	return t
}

// WithOverrides applies the user's accent and density. An empty or invalid
// accent leaves the theme's own. The accent text colour is nudged toward the
// foreground until it reads at 4.5:1, because a bright accent that works as a
// button fill can be unreadable as text on a light page.
func (t Theme) WithOverrides(accent string, density Density) Theme {
	t = t.Filled()
	t.Density = density
	accent = strings.ToLower(strings.TrimSpace(accent))
	if !isHex(accent) {
		return t
	}
	a := mustHex(accent)
	bg, fg := mustHex(t.Colors["window_bg_color"]), mustHex(t.Colors["window_fg_color"])
	t.Secondary = accent
	t.Colors["accent_bg_color"] = accent
	// Label on the fill: whichever of black or white reads better.
	if contrast(a, rgb{255, 255, 255}) >= contrast(a, rgb{}) {
		t.Colors["accent_fg_color"] = "#ffffff"
	} else {
		t.Colors["accent_fg_color"] = "#000000"
	}
	text := a
	for i := 0; i < 20 && contrast(text, bg) < 4.5; i++ {
		text = over(fg, text, 0.15)
	}
	t.Colors["accent_color"] = text.hex()
	return t
}

// Metrics returns the pixel sizes for the theme's density.
func (t Theme) Metrics() Metrics { return MetricsFor(t.Density) }

var (
	builtinOnce sync.Once
	builtinList []Theme
)

// builtinOrder is the picker order, as in Atlas Notes.
var builtinOrder = []string{"light", "dark", "nord", "ember", "sage", "dracula", "rose", "solarized", "ink", "contrast"}

// Builtins returns the ten built-in themes in picker order, derived keys
// filled. The caller owns the result.
func Builtins() []Theme {
	builtinOnce.Do(func() {
		for _, id := range builtinOrder {
			data, err := builtinFS.ReadFile("builtin/" + id + ".json")
			if err != nil {
				panic("theme: missing built-in " + id)
			}
			var t Theme
			if err := json.Unmarshal(data, &t); err != nil {
				panic("theme: bad built-in " + id + ": " + err.Error())
			}
			builtinList = append(builtinList, t.Filled())
		}
	})
	out := make([]Theme, len(builtinList))
	for i, t := range builtinList {
		out[i] = t.clone()
	}
	return out
}

// LoadAll returns the built-ins plus the user's themes from userDir
// (paths.Themes() when empty). A user theme with a built-in's id replaces it
// in place; new ones follow, sorted by file name. A bad file is reported in
// the error list and skipped, never fatal. Two user files with the same id are
// an error naming both; the first by file name wins.
func LoadAll(userDir string) ([]Theme, []error) {
	if userDir == "" {
		userDir = paths.Themes()
	}
	themes := Builtins()
	var errs []error
	ents, err := os.ReadDir(userDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("could not read the themes folder: %w", err))
		}
		return themes, errs
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	seen := map[string]string{} // theme id -> file that claimed it
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(userDir, name))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		var t Theme
		if err := json.Unmarshal(data, &t); err != nil {
			errs = append(errs, fmt.Errorf("%s: not valid JSON: %w", name, err))
			continue
		}
		if err := Validate(t); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if first, dup := seen[t.ID]; dup {
			errs = append(errs, fmt.Errorf("%s and %s both use the theme id %q; using %s", first, name, t.ID, first))
			continue
		}
		seen[t.ID] = name
		t = t.Filled()
		replaced := false
		for i := range themes {
			if themes[i].ID == t.ID {
				themes[i], replaced = t, true
				break
			}
		}
		if !replaced {
			themes = append(themes, t)
		}
	}
	return themes, errs
}

// Remove deletes every theme file in dir that holds the theme id, whatever
// the file is called: a theme copied in by hand need not be named after its
// id. It reports whether any file was removed.
func Remove(dir, id string) (bool, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	removed := false
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var t Theme
		if json.Unmarshal(data, &t) != nil || t.ID != id {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed = true
	}
	return removed, nil
}

// Save writes t to dir/<id>.json atomically after validating it. The id
// becomes a file name, so Validate restricts it to [a-z0-9_-].
func Save(dir string, t Theme) error {
	if err := Validate(t); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, t.ID+".json"), append(data, '\n'), 0o600)
}

// Find returns the theme with the given id from themes. An empty or unknown
// id gives the "light" or "dark" entry by systemDark, so a settings file from
// a newer build never leaves the app unthemed. If themes lacks those, the
// built-ins are used.
func Find(themes []Theme, id string, systemDark bool) Theme {
	for _, t := range themes {
		if id != "" && t.ID == id {
			return t
		}
	}
	fallback := "light"
	if systemDark {
		fallback = "dark"
	}
	for _, t := range themes {
		if t.ID == fallback {
			return t
		}
	}
	for _, t := range Builtins() {
		if t.ID == fallback {
			return t
		}
	}
	panic("theme: built-in " + fallback + " missing")
}

// Resolve is Find over the built-ins only.
func Resolve(id string, systemDark bool) Theme { return Find(Builtins(), id, systemDark) }

// windowsReserved are device names Windows refuses as file names; the id
// becomes <id>.json, so a theme using one could not be saved there.
var windowsReserved = func() map[string]bool {
	m := map[string]bool{"con": true, "prn": true, "aux": true, "nul": true}
	for i := '1'; i <= '9'; i++ {
		m["com"+string(i)], m["lpt"+string(i)] = true, true
	}
	return m
}()

// Validate checks a theme against the family's invariants: well-formed ids and
// colours, the dark flag matching the page's luminance, and readable pairs.
func Validate(t Theme) error {
	if t.ID == "" || t.Name == "" {
		return errors.New("a theme needs an id and a name")
	}
	for _, r := range t.ID {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("the id %q may only use a-z, 0-9, - and _", t.ID)
		}
	}
	if windowsReserved[strings.ToLower(t.ID)] {
		return fmt.Errorf("the id %q is a reserved file name on Windows", t.ID)
	}
	known := map[string]bool{}
	for _, k := range BaseKeys {
		known[k] = true
	}
	for _, k := range DerivedKeys {
		known[k] = true
	}
	for k, v := range t.Colors {
		if !known[k] {
			return fmt.Errorf("unknown colour key %q", k)
		}
		if !isHex(v) {
			return fmt.Errorf("%s: %q is not a lower-case #rrggbb colour", k, v)
		}
	}
	for _, k := range BaseKeys {
		if _, ok := t.Colors[k]; !ok {
			return fmt.Errorf("missing colour %s", k)
		}
	}
	if !isHex(t.Primary) || !isHex(t.Secondary) {
		return errors.New("primary and secondary must be lower-case #rrggbb colours")
	}
	if t.Secondary != t.Colors["accent_bg_color"] {
		return errors.New("secondary must equal accent_bg_color, it is the swatch's promise")
	}
	if dark := mustHex(t.Primary).luminance() < 0.5; dark != t.Dark {
		return fmt.Errorf("the dark flag is %v but the primary colour %s is %s", t.Dark, t.Primary, map[bool]string{true: "dark", false: "light"}[!t.Dark])
	}
	c := func(k string) rgb { return mustHex(t.Colors[k]) }
	for _, surf := range []string{"window", "view", "sidebar", "headerbar", "card", "dialog", "popover"} {
		if r := contrast(c(surf+"_fg_color"), c(surf+"_bg_color")); r < 4.5 {
			return fmt.Errorf("%s text on %s is %.1f:1, needs 4.5:1", surf+"_fg_color", surf+"_bg_color", r)
		}
	}
	if r := contrast(c("accent_fg_color"), c("accent_bg_color")); r < 4.5 {
		return fmt.Errorf("accent_fg_color on accent_bg_color is %.1f:1, needs 4.5:1", r)
	}
	for _, bg := range []string{"window_bg_color", "card_bg_color"} {
		if r := contrast(c("accent_color"), c(bg)); r < 4.5 {
			return fmt.Errorf("accent_color on %s is %.1f:1, needs 4.5:1", bg, r)
		}
	}
	if r := contrast(c("accent_bg_color"), c("window_bg_color")); r < 3 {
		return fmt.Errorf("accent_bg_color on window_bg_color is %.1f:1, needs 3:1", r)
	}
	for _, k := range []string{"warning_color", "error_color", "success_color"} {
		if r := contrast(c(k), c("window_bg_color")); r < 3 {
			return fmt.Errorf("%s on window_bg_color is %.1f:1, needs 3:1", k, r)
		}
	}
	return nil
}

// IsDark is whether a #rrggbb surface counts as dark, by the same rule
// Validate applies to a theme's primary colour.
func IsDark(hex string) bool {
	c, err := parseHex(hex)
	return err == nil && c.luminance() < 0.5
}

// IsBuiltin is whether id names a theme that ships with Commander. A user
// theme may not take one of these ids.
func IsBuiltin(id string) bool {
	for _, t := range Builtins() {
		if t.ID == id {
			return true
		}
	}
	return false
}

// Slug turns a display name into a theme id: lower case, a-z0-9 and dashes.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	return b.String()
}
