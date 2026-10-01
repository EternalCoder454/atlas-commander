// Package pricing turns token counts into dollars.
//
// Prices change, so the table is data: the app ships a default, writes it to
// prices.json on first run, and the user can correct it there. Cost is used
// for budget caps and analytics; where a backend reports its own cost the
// supervisor prefers that and uses this only as a fallback.
//
// Goroutines: a Table is read-only after Load and safe to share. Platform:
// none.
package pricing

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/atomicfile"
	"atlas-commander/internal/paths"
)

//go:embed default.json
var defaultJSON []byte

// Price is USD per million tokens.
type Price struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
}

// Table maps model id prefixes to prices, plus short aliases (opus, sonnet)
// that point at a key of Models.
type Table struct {
	Models  map[string]Price  `json:"models"`
	Aliases map[string]string `json:"aliases,omitempty"`
}

// Default returns the built-in table. It is parsed fresh each call so callers
// may modify it.
func Default() Table {
	var t Table
	if err := json.Unmarshal(defaultJSON, &t); err != nil {
		panic("pricing: embedded default.json is invalid: " + err.Error())
	}
	return t
}

// Lookup finds the price for a model id. The longest matching prefix wins, so
// "claude-opus-5-5-20260101" gets the 5-5 price, not the "claude-opus-5" one.
// A prefix only matches at a boundary (end of id, '-', '@', '[' or '.'), so
// "claude-opus-5" does not claim "claude-opus-50". Aliases are tried only when
// no prefix matches.
//
// ok=false means the cost is unknown, not zero: callers must not show or
// budget it as a free run.
func (t Table) Lookup(model string) (Price, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return Price{}, false
	}
	if p, ok := t.longest(model); ok {
		return p, true
	}
	if target, ok := t.Aliases[model]; ok {
		return t.longest(strings.ToLower(target))
	}
	return Price{}, false
}

// hasBoundaryPrefix reports whether model starts with prefix and the prefix
// ends at a boundary rather than in the middle of a version number.
func hasBoundaryPrefix(model, prefix string) bool {
	if !strings.HasPrefix(model, prefix) {
		return false
	}
	if len(model) == len(prefix) {
		return true
	}
	switch model[len(prefix)] {
	case '-', '@', '[', '.':
		return true
	}
	return false
}

func (t Table) longest(model string) (Price, bool) {
	best, found := "", false
	var price Price
	for prefix, p := range t.Models {
		prefix = strings.ToLower(prefix)
		if hasBoundaryPrefix(model, prefix) && (!found || len(prefix) > len(best)) {
			best, price, found = prefix, p, true
		}
	}
	return price, found
}

// Cost is the dollar cost of u on model. ok is false for an unknown model, in
// which case usd is 0 and the caller should say the cost is unknown rather
// than show a free run.
func (t Table) Cost(model string, u agent.Usage) (usd float64, ok bool) {
	p, ok := t.Lookup(model)
	if !ok {
		return 0, false
	}
	usd = (float64(u.Input)*p.Input +
		float64(u.Output)*p.Output +
		float64(u.CacheRead)*p.CacheRead +
		float64(u.CacheWrite5m)*p.CacheWrite5m +
		float64(u.CacheWrite1h)*p.CacheWrite1h) / 1e6
	return usd, true
}

// Load reads the table at path (paths.Prices() when empty). A missing file is
// created from the default. Bad JSON returns the default and an error, so the
// app keeps working with a note to the user.
func Load(path string) (Table, error) {
	if path == "" {
		path = paths.Prices()
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t := Default()
		if werr := atomicfile.WriteFile(path, defaultJSON, 0o644); werr != nil {
			return t, fmt.Errorf("could not write the default price table: %w", werr)
		}
		return t, nil
	}
	if err != nil {
		return Default(), fmt.Errorf("could not read the price table: %w", err)
	}
	var t Table
	if err := json.Unmarshal(data, &t); err != nil {
		return Default(), fmt.Errorf("the price table is not valid JSON, using the default: %w", err)
	}
	if len(t.Models) == 0 {
		return Default(), errors.New("the price table has no models, using the default")
	}
	for name, p := range t.Models {
		for _, v := range []float64{p.Input, p.Output, p.CacheRead, p.CacheWrite5m, p.CacheWrite1h} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return Default(), fmt.Errorf("the price for %q is negative or not a number, using the default", name)
			}
		}
	}
	return t, nil
}
