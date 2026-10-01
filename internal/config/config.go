// Package config is the user's settings file (settings.json).
//
// It follows Atlas Monitor: defaults first, unmarshal over them, normalise
// every field, so a hand-edited or newer file never produces an unusable
// value. Save keeps keys this build does not know, so switching between a
// beta and a release build does not drop the other's settings.
//
// Goroutines: Load and Save are plain functions with no shared state; the UI
// calls Save from the main thread. Platform: none (file mode 0600 is only
// meaningful on Unix).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"atlas-commander/internal/atomicfile"
	"atlas-commander/internal/paths"
)

// Theme modes.
const (
	ModeSystem = "system"
	ModeLight  = "light"
	ModeDark   = "dark"
)

// Window and font limits.
const (
	MinWindowWidth  = 900
	MinWindowHeight = 600
	MinFontPt       = 8
	MaxFontPt       = 20
)

// Choices for the enum settings, in the order a settings page shows them.
var (
	ThemeModeChoices     = []string{ModeSystem, ModeLight, ModeDark}
	DensityChoices       = []string{"comfortable", "compact"}
	UpdateChannelChoices = []string{"release", "beta"}
)

// Window transparency levels, as Atlas Monitor has them. Off leaves the window
// opaque, which is how Commander has always looked.
const (
	TransparencyOff    = "off"
	TransparencySubtle = "subtle"
	TransparencyMedium = "medium"
	TransparencyStrong = "strong"
)

// TransparencyChoices are the levels Settings offers, weakest first.
var TransparencyChoices = []string{TransparencyOff, TransparencySubtle, TransparencyMedium, TransparencyStrong}

// Settings is everything the user can change.
type Settings struct {
	Theme     string `json:"theme"` // theme id; "" follows the system
	ThemeMode string `json:"theme_mode"`
	Accent    string `json:"accent"` // "#rrggbb" override or ""
	Density   string `json:"density"`
	UIFont    string `json:"ui_font"` // "" = the system font
	MonoFont  string `json:"mono_font"`
	FontSize  int    `json:"font_size"` // points
	// Transparency is the window glass level: off, subtle, medium or strong.
	Transparency  string `json:"transparency"`
	WindowWidth   int    `json:"window_width"`
	WindowHeight  int    `json:"window_height"`
	ActiveView    string `json:"active_view"`
	Notifications bool   `json:"notifications"`
	// ShowIntro plays the Atlas mark and the app's name when the window
	// opens. It is skipped anyway when the desktop has animations turned off.
	ShowIntro     bool   `json:"show_intro"`
	UpdateChannel string `json:"update_channel"`
	UpdateCheck   bool   `json:"update_check"`
	ClaudePath    string `json:"claude_path"` // "" = look on PATH
	APIKeyEnv     string `json:"api_key_env"` // env var holding the API key
	DefaultFleet  string `json:"default_fleet"`
}

// Defaults is what a fresh install uses.
func Defaults() Settings {
	return Settings{
		ThemeMode:     ModeSystem,
		Density:       "comfortable",
		Transparency:  TransparencyOff,
		MonoFont:      "JetBrains Mono",
		FontSize:      10,
		WindowWidth:   1440,
		WindowHeight:  900,
		Notifications: true,
		ShowIntro:     true,
		UpdateChannel: "release",
		UpdateCheck:   true,
		APIKeyEnv:     "ANTHROPIC_API_KEY",
	}
}

// pick returns v if it is one of choices, else def.
func pick(v string, choices []string, def string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, c := range choices {
		if v == c {
			return v
		}
	}
	return def
}

// NormalizeThemeMode maps junk to "system".
func NormalizeThemeMode(v string) string { return pick(v, ThemeModeChoices, ModeSystem) }

// NormalizeDensity maps junk to "comfortable".
func NormalizeDensity(v string) string { return pick(v, DensityChoices, "comfortable") }

// NormalizeUpdateChannel maps junk to "release".
func NormalizeUpdateChannel(v string) string { return pick(v, UpdateChannelChoices, "release") }

// NormalizeTransparency maps anything but a known level to "off", so a
// hand-edited value can never leave the window half see-through with no way
// to see why.
func NormalizeTransparency(v string) string { return pick(v, TransparencyChoices, TransparencyOff) }

// NormalizeAccent keeps a lower-case #rrggbb and drops anything else, so a bad
// value means "use the theme's accent" rather than a broken style sheet.
func NormalizeAccent(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != 7 || v[0] != '#' {
		return ""
	}
	for _, c := range v[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return v
}

// NormalizeFontSize clamps to MinFontPt..MaxFontPt; 0 (unset) gives the default.
func NormalizeFontSize(v int) int {
	if v == 0 {
		return Defaults().FontSize
	}
	return min(max(v, MinFontPt), MaxFontPt)
}

// Normalize fixes every field that has a rule. It never fails.
func (s *Settings) Normalize() {
	d := Defaults()
	s.Theme = strings.TrimSpace(s.Theme)
	s.ThemeMode = NormalizeThemeMode(s.ThemeMode)
	s.Accent = NormalizeAccent(s.Accent)
	s.Density = NormalizeDensity(s.Density)
	s.Transparency = NormalizeTransparency(s.Transparency)
	s.UIFont = strings.TrimSpace(s.UIFont)
	if s.MonoFont = strings.TrimSpace(s.MonoFont); s.MonoFont == "" {
		s.MonoFont = d.MonoFont
	}
	s.FontSize = NormalizeFontSize(s.FontSize)
	s.WindowWidth = max(s.WindowWidth, MinWindowWidth)
	s.WindowHeight = max(s.WindowHeight, MinWindowHeight)
	s.UpdateChannel = NormalizeUpdateChannel(s.UpdateChannel)
	s.ClaudePath = strings.TrimSpace(s.ClaudePath)
	if s.APIKeyEnv = strings.TrimSpace(s.APIKeyEnv); s.APIKeyEnv == "" {
		s.APIKeyEnv = d.APIKeyEnv
	}
}

// Load reads path (paths.Settings() when empty). A missing file gives the
// defaults with no error. A file that is not valid JSON is renamed to
// settings.json.bad and gives the defaults and an error, so the app starts and
// can tell the user. A wrongly typed field keeps the rest of the settings.
func Load(path string) (Settings, error) {
	if path == "" {
		path = paths.Settings()
	}
	s := Defaults()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("could not read settings: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			// Not JSON at all. Set the file aside before the next Save
			// overwrites it, so the user can still recover their settings.
			bad := path + ".bad"
			if rerr := os.Rename(path, bad); rerr != nil {
				return Defaults(), fmt.Errorf("the settings file was unreadable and could not be set aside: %w", rerr)
			}
			return Defaults(), fmt.Errorf("the settings file was unreadable and was set aside as %s; using defaults", filepath.Base(bad))
		}
		// One wrongly typed field: Unmarshal has filled in every other
		// field, so keep them and let Normalize repair the one it skipped.
	}
	s.Normalize()
	return s, nil
}

// Save writes s to path (paths.Settings() when empty).
//
// The write goes to a temp file in the same directory and is renamed over the
// target: the app saves on close, when it is most likely to be killed, and a
// truncated file would silently reset every setting. Mode 0600 because
// nothing else needs to read it.
func Save(path string, s Settings) error {
	if path == "" {
		path = paths.Settings()
	}
	s.Normalize()
	b, err := merged(path, s)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, b, 0o600)
}

// merged is s as JSON on top of whatever keys the existing file holds that
// this build does not know, so a newer build's settings survive our save.
func merged(path string, s Settings) ([]byte, error) {
	fresh, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if old, err := os.ReadFile(path); err == nil {
		// A damaged file has nothing to keep, and `null` would leave the map nil.
		if json.Unmarshal(old, &out) != nil || out == nil {
			out = map[string]json.RawMessage{}
		}
	}
	var mine map[string]json.RawMessage
	if err := json.Unmarshal(fresh, &mine); err != nil {
		return nil, err
	}
	for k, v := range mine {
		out[k] = v
	}
	return json.MarshalIndent(out, "", "  ")
}
