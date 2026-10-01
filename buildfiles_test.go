package atlascommander

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The Makefile and the justfile are two front doors to the same build: README,
// setup.sh and the updater use whichever tool is installed. A recipe that
// exists in one and not the other works for some people and fails for others,
// so their names must match.
func TestMakefileAndJustfileHaveTheSameRecipes(t *testing.T) {
	mk := recipes(t, "Makefile", regexp.MustCompile(`(?m)^([a-z][a-z0-9-]*):`))
	jf := recipes(t, "justfile", regexp.MustCompile(`(?m)^([a-z][a-z0-9-]*)( [^:=]*)?:[^=]`))
	if !slices.Equal(mk, jf) {
		t.Errorf("got Makefile %v, justfile %v, want the same recipes", mk, jf)
	}
}

func recipes(t *testing.T, file string, re *regexp.Regexp) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		if !strings.HasPrefix(m[1], ".") {
			names = append(names, m[1])
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
