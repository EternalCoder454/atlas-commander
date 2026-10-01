package pricing

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atlas-commander/internal/agent"
)

// A real Claude Code run reported total_cost_usd 0.0697986 for this usage; if
// our arithmetic drifts from that, budget caps drift from what Anthropic bills.
func TestCostMatchesClaudeCodeRun(t *testing.T) {
	u := agent.Usage{Input: 18, Output: 222, CacheRead: 32526, CacheWrite1h: 32709}
	got, ok := Default().Cost("claude-haiku-4-5-20251001", u)
	if !ok {
		t.Fatal("haiku not found")
	}
	if want := 0.0697986; math.Abs(got-want) > 1e-9 {
		t.Errorf("got %.10f, want %.10f", got, want)
	}
}

// Dated model ids must hit the right row, and "claude-opus-5" must not steal
// "claude-opus-5-5-..." because the two have different prices.
func TestLookupLongestPrefixWins(t *testing.T) {
	tb := Default()
	p, _ := tb.Lookup("claude-opus-5-5-20260101")
	if p.Input != 4 {
		t.Errorf("opus-5-5: got input %v, want 4", p.Input)
	}
	p, _ = tb.Lookup("claude-opus-5-20260101")
	if p.Input != 5 {
		t.Errorf("opus-5: got input %v, want 5", p.Input)
	}
}

// Aliases are what users type in agent settings.
func TestLookupAliasesAndUnknown(t *testing.T) {
	tb := Default()
	if p, ok := tb.Lookup("sonnet"); !ok || p.Input != 2 {
		t.Errorf("sonnet alias: got %v %v, want input 2", p, ok)
	}
	if p, ok := tb.Lookup("fable"); !ok || p.CacheRead != 0.25 {
		t.Errorf("fable alias: got %v %v, want read 0.25", p, ok)
	}
	if _, ok := tb.Lookup("gpt-9"); ok {
		t.Error("unknown model: got ok, want not ok")
	}
	if _, ok := tb.Cost("", agent.Usage{Input: 1}); ok {
		t.Error("empty model: got ok, want not ok")
	}
}

// First run must leave an editable file behind; a later edit must be honoured.
func TestLoadWritesDefaultThenReadsEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "prices.json")
	tb, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tb.Lookup("haiku"); !ok {
		t.Error("default table lacks haiku")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default not written: %v", err)
	}
	os.WriteFile(path, []byte(`{"models":{"x-1":{"input":7}}}`), 0o600)
	tb, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := tb.Lookup("x-1-foo"); !ok || p.Input != 7 {
		t.Errorf("got %v %v, want input 7", p, ok)
	}
}

// A hand-edited typo must not stop the app: default plus an error.
func TestLoadBadJSONFallsBackToDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	os.WriteFile(path, []byte("{nope"), 0o600)
	tb, err := Load(path)
	if err == nil {
		t.Error("got nil error, want one")
	}
	if _, ok := tb.Lookup("haiku"); !ok {
		t.Error("fallback table lacks haiku")
	}
}

// "claude-opus-5" must not price "claude-opus-50": a wrong prefix hit would
// silently bill a different model's rate.
func TestLookupPrefixNeedsBoundary(t *testing.T) {
	tb := Table{Models: map[string]Price{"claude-opus-5": {Input: 5}}}
	for model, want := range map[string]bool{
		"claude-opus-5":          true,
		"claude-opus-5-20260101": true,
		"claude-opus-5@x":        true,
		"claude-opus-5[1m]":      true,
		"claude-opus-5.1":        true,
		"claude-opus-50":         false,
		"claude-opus-5x":         false,
	} {
		if _, ok := tb.Lookup(model); ok != want {
			t.Errorf("Lookup(%q): got %v, want %v", model, ok, want)
		}
	}
}

// A negative price would turn spending into credit and defeat budget caps.
func TestLoadRejectsNegativePrices(t *testing.T) {
	p := filepath.Join(t.TempDir(), "prices.json")
	os.WriteFile(p, []byte(`{"models":{"claude-bad":{"input":-1,"output":1}}}`), 0o600)
	tb, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "claude-bad") {
		t.Fatalf("got err %v, want one naming claude-bad", err)
	}
	if _, ok := tb.Lookup("claude-opus-5"); !ok {
		t.Error("got no default table after rejection")
	}
}

// The older models are still in use and must have a price, or their cost
// shows as unknown.
func TestDefaultHasOlderModels(t *testing.T) {
	for _, m := range []string{"claude-sonnet-4-5-20250929", "claude-sonnet-4-20250514", "claude-opus-4-1-20250805", "claude-opus-4-20250514", "claude-haiku-3-5-20241022"} {
		if _, ok := Default().Lookup(m); !ok {
			t.Errorf("Lookup(%q): got unknown, want a price", m)
		}
	}
	if p, _ := Default().Lookup("claude-opus-4-1"); p.Input != 15 {
		t.Errorf("opus-4-1 input: got %v, want 15", p.Input)
	}
}
