package demo

import (
	"testing"

	"atlas-commander/internal/store"
	"atlas-commander/internal/ui"
)

var _ ui.Controller = (*Fleet)(nil)

func TestSnapshot(t *testing.T) {
	f := New()
	defer f.Close()
	s := f.Snapshot()
	if len(s.Agents) == 0 || len(s.Fleets) != 2 || len(s.Tasks) == 0 {
		t.Fatalf("snapshot: %d agents, %d fleets, %d tasks", len(s.Agents), len(s.Fleets), len(s.Tasks))
	}
	tot, _ := f.Totals(storeFilter())
	if tot.CostUSD <= 0 || tot.Results == 0 {
		t.Fatalf("totals: %+v", tot)
	}
}

func storeFilter() store.Filter { return store.Filter{} }
