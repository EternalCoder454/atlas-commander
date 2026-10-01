package theme

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IndicatorDir is where WriteIndicators puts the tick images the style sheet
// draws in checked boxes. Qt style sheets take images only from files or
// compiled resources, so the UI writes them at start-up. When it is empty
// checked boxes are a plain accent square.
var IndicatorDir string

func checkPath(fg string) string {
	return filepath.ToSlash(filepath.Join(IndicatorDir, "check-"+strings.TrimPrefix(fg, "#")+".svg"))
}

// WriteIndicators writes the tick for text colour fg into IndicatorDir if it
// is not there yet.
func WriteIndicators(fg string) error {
	if IndicatorDir == "" || !isHex(fg) {
		return nil
	}
	path := checkPath(fg)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(IndicatorDir, 0o700); err != nil {
		return err
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">`+
		`<path d="M4 8.2 6.8 11 12.2 5.2" fill="none" stroke="%s" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>`, fg)
	return os.WriteFile(path, []byte(svg), 0o600)
}

func chevronPath(fg string) string {
	return filepath.ToSlash(filepath.Join(IndicatorDir, "chevron-"+strings.TrimPrefix(fg, "#")+".svg"))
}

// WriteChevron writes the dropdown arrow for text colour fg into
// IndicatorDir if it is not there yet. It is Material Symbols'
// keyboard_arrow_down.
func WriteChevron(fg string) error {
	if IndicatorDir == "" || !isHex(fg) {
		return nil
	}
	path := chevronPath(fg)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(IndicatorDir, 0o700); err != nil {
		return err
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 -960 960 960">`+
		`<path d="M480-333 240-573l51-51 189 189 189-189 51 51-240 240Z" fill="%s" fill-opacity="0.75"/></svg>`, fg)
	return os.WriteFile(path, []byte(svg), 0o600)
}
