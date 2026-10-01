package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Junk in a hand-edited file must land on a usable value, not crash the UI.
func TestNormalizeFixesJunk(t *testing.T) {
	s := Settings{
		ThemeMode: "purple", Accent: "red", Density: "tiny", FontSize: 99,
		WindowWidth: 10, WindowHeight: 10, UpdateChannel: "nightly",
	}
	s.Normalize()
	d := Defaults()
	if s.ThemeMode != d.ThemeMode || s.Density != d.Density || s.UpdateChannel != d.UpdateChannel {
		t.Errorf("enums: got %q %q %q, want defaults", s.ThemeMode, s.Density, s.UpdateChannel)
	}
	if s.Accent != "" {
		t.Errorf("accent: got %q, want empty", s.Accent)
	}
	if s.FontSize != MaxFontPt {
		t.Errorf("font size: got %d, want %d", s.FontSize, MaxFontPt)
	}
	if s.WindowWidth != MinWindowWidth || s.WindowHeight != MinWindowHeight {
		t.Errorf("window: got %dx%d, want %dx%d", s.WindowWidth, s.WindowHeight, MinWindowWidth, MinWindowHeight)
	}
	if s.MonoFont != "JetBrains Mono" || s.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("empty strings: got %q %q, want defaults", s.MonoFont, s.APIKeyEnv)
	}
	s.FontSize = 3
	s.Normalize()
	if s.FontSize != MinFontPt {
		t.Errorf("font size: got %d, want %d", s.FontSize, MinFontPt)
	}
}

// Every value a choices slice offers must survive normalising, or the settings
// page would show a choice that does not stick.
func TestNormalizeKeepsEveryChoice(t *testing.T) {
	for _, c := range ThemeModeChoices {
		if got := NormalizeThemeMode(c); got != c {
			t.Errorf("theme mode %q: got %q", c, got)
		}
	}
	for _, c := range DensityChoices {
		if got := NormalizeDensity(c); got != c {
			t.Errorf("density %q: got %q", c, got)
		}
	}
	for _, c := range UpdateChannelChoices {
		if got := NormalizeUpdateChannel(c); got != c {
			t.Errorf("channel %q: got %q", c, got)
		}
	}
	if got := NormalizeAccent("#AABBCC"); got != "#aabbcc" {
		t.Errorf("accent: got %q, want #aabbcc", got)
	}
}

// A new key added to Settings later must not read as zero from an old file.
func TestLoadMissingFileAndPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Load(path)
	if err != nil || s != Defaults() {
		t.Fatalf("missing: got %+v, %v, want defaults", s, err)
	}
	os.WriteFile(path, []byte(`{"density":"compact"}`), 0o600)
	s, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Density != "compact" || !s.Notifications || s.FontSize != 10 {
		t.Errorf("got %+v, want compact with other defaults kept", s)
	}
}

// A corrupt file must not stop the app starting.
func TestLoadCorruptGivesDefaultsAndError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte("{broken"), 0o600)
	s, err := Load(path)
	if err == nil {
		t.Error("got nil error, want one")
	}
	if s != Defaults() {
		t.Errorf("got %+v, want defaults", s)
	}
}

// Set aside, not overwritten: the next Save would otherwise destroy the only
// copy of a file the user may be able to fix by hand.
func TestLoadSyntaxErrorSetsFileAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path+".bad", []byte("older"), 0o600)
	os.WriteFile(path, []byte("{broken"), 0o600)
	s, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "set aside") {
		t.Fatalf("got err %v, want one saying the file was set aside", err)
	}
	if s != Defaults() {
		t.Errorf("got %+v, want defaults", s)
	}
	if b, _ := os.ReadFile(path + ".bad"); string(b) != "{broken" {
		t.Errorf(".bad: got %q, want the broken file", b)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original: got %v, want it moved away", err)
	}
}

// One wrongly typed field must not reset the other settings.
func TestLoadTypeErrorKeepsOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"font_size":"big","density":"compact","window_width":1500}`), 0o600)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Density != "compact" || s.WindowWidth != 1500 {
		t.Errorf("got %+v, want density compact and width 1500 kept", s)
	}
	if s.FontSize != Defaults().FontSize {
		t.Errorf("font size: got %d, want the default", s.FontSize)
	}
	if _, err := os.Stat(path + ".bad"); err == nil {
		t.Error("got a .bad file, want none for a type error")
	}
}

// A newer build's keys must survive our save, or switching channels loses settings.
func TestSaveKeepsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"future_key":{"a":1},"density":"comfortable"}`), 0o600)
	s, _ := Load(path)
	s.Density = "compact"
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	var fk map[string]int
	if err := json.Unmarshal(m["future_key"], &fk); err != nil || fk["a"] != 1 {
		t.Errorf("future_key: got %s, want {\"a\":1}", m["future_key"])
	}
	if string(m["density"]) != `"compact"` {
		t.Errorf("density: got %s, want compact", m["density"])
	}
	back, _ := Load(path)
	if back != s {
		t.Errorf("round trip: got %+v, want %+v", back, s)
	}
}

// A crash between write and rename must not leave litter beside the settings.
func TestSaveLeavesNoTempFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	path := filepath.Join(dir, "settings.json")
	if err := Save(path, Defaults()); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "settings.json" {
		t.Errorf("got %d entries %v, want only settings.json", len(ents), ents)
	}
}

// Empty path means the real location, which tests must redirect.
func TestEmptyPathUsesPathsPackage(t *testing.T) {
	t.Setenv("ATLAS_CONFIG_HOME", t.TempDir())
	if err := Save("", Defaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(""); err != nil {
		t.Fatal(err)
	}
}
