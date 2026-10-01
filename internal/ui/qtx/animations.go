//go:build !windows

package qtx

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AnimationsEnabled reports whether the desktop allows animation. Someone
// who has turned animations off is not shown one at startup either. Qt has
// no setting for this, so it reads the desktops' own: KDE's animation speed
// (0 is off) and GTK's gtk-enable-animations, which GNOME's switch writes.
func AnimationsEnabled() bool {
	if os.Getenv("ATLAS_NO_ANIMATIONS") != "" {
		return false
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return true
		}
		cfg = filepath.Join(home, ".config")
	}
	if v, ok := iniValue(filepath.Join(cfg, "kdeglobals"), "KDE", "AnimationDurationFactor"); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f == 0 {
			return false
		}
	}
	for _, gtk := range []string{"gtk-4.0", "gtk-3.0"} {
		if v, ok := iniValue(filepath.Join(cfg, gtk, "settings.ini"), "Settings", "gtk-enable-animations"); ok {
			switch strings.ToLower(v) {
			case "0", "false":
				return false
			}
			break
		}
	}
	return true
}

// iniValue reads one key from one section of an INI file.
func iniValue(path, section, key string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "["+section+"]"
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}
