package theme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The built-ins are the family's promise; a palette that fails contrast or
// whose flag lies would ship unreadable text.
func TestBuiltinsValidate(t *testing.T) {
	bs := Builtins()
	if len(bs) != 10 {
		t.Fatalf("got %d built-ins, want 10", len(bs))
	}
	for _, th := range bs {
		if err := Validate(th); err != nil {
			t.Errorf("%s: %v", th.ID, err)
		}
	}
}

// Derived keys must exist after loading so the UI never needs a fallback, and a
// light theme's chart colours must be darker than a dark theme's.
func TestFilledDerivesMissingKeys(t *testing.T) {
	for _, th := range Builtins() {
		for _, k := range DerivedKeys {
			if !isHex(th.Colors[k]) {
				t.Errorf("%s: %s = %q, want a hex colour", th.ID, k, th.Colors[k])
			}
		}
	}
	dark, light := Resolve("dark", true), Resolve("light", false)
	if dark.Colors["chart_1"] != "#39b8e3" {
		t.Errorf("dark chart_1: got %s, want #39b8e3", dark.Colors["chart_1"])
	}
	if light.Colors["chart_1"] == "#39b8e3" || mustHex(light.Colors["chart_1"]).luminance() >= mustHex("#39b8e3").luminance() {
		t.Errorf("light chart_1: got %s, want darker than #39b8e3", light.Colors["chart_1"])
	}
	// A key the theme sets itself is not overwritten.
	th := Resolve("nord", true)
	th.Colors = map[string]string{}
	for k, v := range Resolve("nord", true).Colors {
		th.Colors[k] = v
	}
	th.Colors["grid_color"] = "#123456"
	if got := th.Filled().Colors["grid_color"]; got != "#123456" {
		t.Errorf("grid_color: got %s, want kept", got)
	}
}

// Validate must reject the mistakes a hand-edited theme is likely to have.
func TestValidateRejectsBadThemes(t *testing.T) {
	good := Resolve("nord", true)
	edit := func(f func(*Theme)) Theme {
		th := good.clone()
		f(&th)
		return th
	}
	cases := map[string]Theme{
		"uppercase hex":  edit(func(t *Theme) { t.Colors["window_bg_color"] = "#2E3440" }),
		"missing key":    edit(func(t *Theme) { delete(t.Colors, "card_fg_color") }),
		"unknown key":    edit(func(t *Theme) { t.Colors["bogus"] = "#000000" }),
		"wrong dark":     edit(func(t *Theme) { t.Dark = false }),
		"low contrast":   edit(func(t *Theme) { t.Colors["window_fg_color"] = "#33394a" }),
		"bad id":         edit(func(t *Theme) { t.ID = "../x" }),
		"reserved id":    edit(func(t *Theme) { t.ID = "con" }),
		"reserved com9":  edit(func(t *Theme) { t.ID = "com9" }),
		"secondary diff": edit(func(t *Theme) { t.Secondary = "#000000" }),
		"weak status":    edit(func(t *Theme) { t.Colors["error_color"] = "#303642" }),
	}
	for name, th := range cases {
		if Validate(th) == nil {
			t.Errorf("%s: got nil, want an error", name)
		}
	}
}

// A saved theme must read back identical, or editing a theme would drift it.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	th := Resolve("sage", false)
	th.ID, th.Name = "mine", "Mine"
	if err := Save(dir, th); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "mine.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back Theme
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, th) {
		t.Errorf("round trip: got %+v, want %+v", back, th)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("got %d files, want 1 (no temp left)", len(ents))
	}
}

// A user theme with a built-in's id replaces it, new ones are added, and a
// broken file is reported without hiding the rest.
func TestLoadAllUserOverrideAndErrors(t *testing.T) {
	dir := t.TempDir()
	over := Resolve("nord", true)
	over.Name = "My Nord"
	if err := Save(dir, over); err != nil {
		t.Fatal(err)
	}
	extra := Resolve("ember", true)
	extra.ID, extra.Name = "extra", "Extra"
	if err := Save(dir, extra); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600)
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"id":"bad","name":"Bad"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600)

	themes, errs := LoadAll(dir)
	if len(errs) != 2 {
		t.Errorf("got %d errors %v, want 2", len(errs), errs)
	}
	if len(themes) != 11 {
		t.Fatalf("got %d themes, want 11", len(themes))
	}
	if themes[2].ID != "nord" || themes[2].Name != "My Nord" {
		t.Errorf("slot 2: got %s %q, want overridden nord", themes[2].ID, themes[2].Name)
	}
	if themes[10].ID != "extra" {
		t.Errorf("last: got %s, want extra", themes[10].ID)
	}
	// A missing folder is normal.
	if themes, errs := LoadAll(filepath.Join(dir, "none")); len(themes) != 10 || len(errs) != 0 {
		t.Errorf("missing dir: got %d themes %v, want 10 and no errors", len(themes), errs)
	}
}

// A settings file from a newer build may name a theme this one lacks; it
// must still land on something matching the system scheme.
func TestResolveFallbacks(t *testing.T) {
	cases := []struct {
		id   string
		dark bool
		want string
	}{
		{"", false, "light"}, {"", true, "dark"}, {"nope", true, "dark"},
		{"nope", false, "light"}, {"dracula", false, "dracula"},
	}
	for _, c := range cases {
		if got := Resolve(c.id, c.dark).ID; got != c.want {
			t.Errorf("Resolve(%q,%v): got %s, want %s", c.id, c.dark, got, c.want)
		}
	}
}

// The accent override must change the fill and the label, and keep the accent
// text readable on the page.
func TestWithOverrides(t *testing.T) {
	base := Resolve("light", false)
	th := base.WithOverrides("#ffd60a", Compact)
	if th.Colors["accent_bg_color"] != "#ffd60a" || th.Secondary != "#ffd60a" {
		t.Errorf("accent: got %s / %s, want #ffd60a", th.Colors["accent_bg_color"], th.Secondary)
	}
	if th.Colors["accent_fg_color"] != "#000000" {
		t.Errorf("label on yellow: got %s, want #000000", th.Colors["accent_fg_color"])
	}
	if r := contrast(mustHex(th.Colors["accent_color"]), mustHex(th.Colors["window_bg_color"])); r < 4.5 {
		t.Errorf("accent text contrast: got %.2f, want >= 4.5", r)
	}
	if th.Metrics().RowHeight != 40 {
		t.Errorf("compact row: got %d, want 40", th.Metrics().RowHeight)
	}
	if base.Colors["accent_bg_color"] == "#ffd60a" {
		t.Error("override changed the original")
	}
	if same := base.WithOverrides("junk", Comfortable); same.Colors["accent_bg_color"] != base.Colors["accent_bg_color"] {
		t.Error("junk accent changed the theme")
	}
	if got := MetricsFor(Comfortable); got.RowHeight != 44 || got.Radius != 7 || got.PageRadius != 10 || got.SmallRadius != 5 || got.Hairline != 1 {
		t.Errorf("comfortable metrics: got %+v", got)
	}
}

// Every colour the theme defines should be reachable in the style sheet, and
// the output must be byte-identical across calls so the UI can skip re-polishing.
func TestQSSDeterministicAndUsesEveryColour(t *testing.T) {
	for _, th := range Builtins() {
		m := MetricsFor(Comfortable)
		a := QSS(th, m, "Inter", "JetBrains Mono", 10)
		if b := QSS(th, m, "Inter", "JetBrains Mono", 10); a != b {
			t.Errorf("%s: output differs between calls", th.ID)
		}
		for _, k := range append(append([]string{}, BaseKeys...), DerivedKeys...) {
			if !strings.Contains(a, th.Colors[k]) {
				t.Errorf("%s: style sheet lacks %s (%s)", th.ID, k, th.Colors[k])
			}
		}
		for _, want := range []string{"#sidebar", "#page", `[accent="true"]`, "rgba(", `"Inter"`, `"JetBrains Mono"`, "10pt"} {
			if !strings.Contains(a, want) {
				t.Errorf("%s: style sheet lacks %q", th.ID, want)
			}
		}
		if strings.Contains(a, "shadow") {
			t.Errorf("%s: style sheet has a shadow", th.ID)
		}
	}
}

// The Qt side builds a QPalette from this map and indexes it by role, so every
// role must be present and be a hex colour.
func TestPaletteHasEveryRole(t *testing.T) {
	roles := []string{"Window", "WindowText", "Base", "AlternateBase", "Text", "Button", "ButtonText", "Highlight", "HighlightedText", "ToolTipBase", "ToolTipText", "PlaceholderText", "Link", "Mid", "Dark", "Light"}
	p := Palette(Resolve("dracula", true))
	for _, r := range roles {
		if !isHex(p[r]) {
			t.Errorf("%s: got %q, want a hex colour", r, p[r])
		}
	}
	if len(p) != len(roles) {
		t.Errorf("got %d roles, want %d", len(p), len(roles))
	}
	if p["Window"] != "#282a36" || p["Highlight"] != "#bd93f9" {
		t.Errorf("dracula: got Window %s Highlight %s", p["Window"], p["Highlight"])
	}
}

// Two files with one id would make the winner depend on directory order; the
// user must be told which files clash.
func TestLoadAllReportsDuplicateUserThemes(t *testing.T) {
	dir := t.TempDir()
	th := Resolve("ember", true)
	th.ID, th.Name = "twin", "Twin"
	if err := Save(dir, th); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(th)
	os.WriteFile(filepath.Join(dir, "zcopy.json"), data, 0o600)
	themes, errs := LoadAll(dir)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "twin.json") || !strings.Contains(errs[0].Error(), "zcopy.json") {
		t.Fatalf("got %v, want one error naming twin.json and zcopy.json", errs)
	}
	if len(themes) != 11 {
		t.Errorf("got %d themes, want 11", len(themes))
	}
}
