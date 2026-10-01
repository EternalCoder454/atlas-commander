//go:build !windows

package qtx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnimationsEnabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ATLAS_NO_ANIMATIONS", "")
	if !AnimationsEnabled() {
		t.Fatal("no settings should mean animations are on")
	}
	os.WriteFile(filepath.Join(dir, "kdeglobals"), []byte("[General]\nAnimationDurationFactor=0\n[KDE]\nAnimationDurationFactor=0.5\n"), 0o600)
	if !AnimationsEnabled() {
		t.Fatal("KDE factor 0.5 is on")
	}
	os.WriteFile(filepath.Join(dir, "kdeglobals"), []byte("[KDE]\nAnimationDurationFactor=0\n"), 0o600)
	if AnimationsEnabled() {
		t.Fatal("KDE factor 0 is off")
	}
	os.Remove(filepath.Join(dir, "kdeglobals"))
	os.MkdirAll(filepath.Join(dir, "gtk-4.0"), 0o700)
	os.WriteFile(filepath.Join(dir, "gtk-4.0", "settings.ini"), []byte("[Settings]\ngtk-enable-animations=false\n"), 0o600)
	if AnimationsEnabled() {
		t.Fatal("GTK animations off")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ATLAS_NO_ANIMATIONS", "1")
	if AnimationsEnabled() {
		t.Fatal("ATLAS_NO_ANIMATIONS turns them off")
	}
}
