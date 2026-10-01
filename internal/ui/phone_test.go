package ui

import (
	"strings"
	"testing"
)

// The Settings page shows the pairing link in a word-wrapped label; without
// break points the label is as wide as the link and the page runs off the
// window. The breaks must also never change the link itself.
func TestBreakableAddsBreaksWithoutChangingTheLink(t *testing.T) {
	link := "atlascommander://pair?v=1&n=desktop&h=192.168.1.20:47821&t=abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG&f=" + strings.Repeat("ab", 32)
	got := breakable(link)
	if back := strings.ReplaceAll(got, "​", ""); back != link {
		t.Errorf("got %q without breaks, want %q", back, link)
	}
	for _, part := range strings.Split(got, "​") {
		if n := len([]rune(part)); n > 8 {
			t.Errorf("got an unbroken run of %d characters, want at most 8", n)
		}
	}
	if got := breakable(""); got != "" {
		t.Errorf("got %q for an empty link, want empty", got)
	}
}
