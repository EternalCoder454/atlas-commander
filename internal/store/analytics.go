package store

import (
	"time"

	"atlas-commander/internal/agent"
)

// Totals is what the audit log says about a set of rows. Everything here is
// computed from the audit table alone; nothing is stored.
type Totals struct {
	Usage     agent.Usage
	CostUSD   float64
	Results   int // "result" rows: finished turns
	Successes int // results that were not errors
	ToolCalls int // "tool_use" rows
	Errors    int // rows flagged as errors, of any kind
}

// SuccessRate is Successes/Results, or 0 when there are no results.
func (t Totals) SuccessRate() float64 {
	if t.Results == 0 {
		return 0
	}
	return float64(t.Successes) / float64(t.Results)
}

// OutputPerUSD is output tokens per dollar, the token-efficiency measure; 0 when nothing was spent.
func (t Totals) OutputPerUSD() float64 {
	if t.CostUSD <= 0 {
		return 0
	}
	return float64(t.Usage.Output) / t.CostUSD
}

// CostPerSuccess is dollars per successful result; 0 when there were none.
func (t Totals) CostPerSuccess() float64 {
	if t.Successes == 0 {
		return 0
	}
	return t.CostUSD / float64(t.Successes)
}

// AgentStats is Totals for one agent.
type AgentStats struct {
	AgentID string
	Totals
}

// SessionStats is Totals for one backend session.
type SessionStats struct {
	SessionID string
	AgentID   string
	FleetID   string
	First     time.Time
	Last      time.Time
	Totals
}

// totalsCols are the aggregate columns, in the order scanTotals reads them.
const totalsCols = `COALESCE(SUM(input),0), COALESCE(SUM(output),0), COALESCE(SUM(cache_read),0),
	COALESCE(SUM(cache_write_5m),0), COALESCE(SUM(cache_write_1h),0), COALESCE(SUM(cost_usd),0),
	COALESCE(SUM(kind='result'),0), COALESCE(SUM(kind='result' AND is_error=0),0),
	COALESCE(SUM(kind='tool_use'),0), COALESCE(SUM(is_error),0)`

func totalsDest(t *Totals) []any {
	return []any{&t.Usage.Input, &t.Usage.Output, &t.Usage.CacheRead, &t.Usage.CacheWrite5m, &t.Usage.CacheWrite1h,
		&t.CostUSD, &t.Results, &t.Successes, &t.ToolCalls, &t.Errors}
}

// FleetTotals is the sum over everything the filter matches.
func (s *Store) FleetTotals(f Filter) (Totals, error) {
	w, args := f.where()
	var t Totals
	err := s.db.QueryRow(`SELECT `+totalsCols+` FROM audit`+w, args...).Scan(totalsDest(&t)...)
	return t, err
}

// AgentStats returns one row per agent with audit rows in the filter,
// costliest first. Rows with no agent are left out.
func (s *Store) AgentStats(f Filter) ([]AgentStats, error) {
	w, args := f.where()
	if w == "" {
		w = " WHERE agent_id<>''"
	} else {
		w += " AND agent_id<>''"
	}
	rows, err := s.db.Query(`SELECT agent_id, `+totalsCols+` FROM audit`+w+` GROUP BY agent_id ORDER BY SUM(cost_usd) DESC, agent_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentStats
	for rows.Next() {
		var a AgentStats
		if err := rows.Scan(append([]any{&a.AgentID}, totalsDest(&a.Totals)...)...); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SessionStats returns one row per session, newest first. Rows with no
// session are left out.
func (s *Store) SessionStats(f Filter) ([]SessionStats, error) {
	w, args := f.where()
	if w == "" {
		w = " WHERE session_id<>''"
	} else {
		w += " AND session_id<>''"
	}
	rows, err := s.db.Query(`SELECT session_id, MAX(agent_id), MAX(fleet_id), MIN(at), MAX(at), `+totalsCols+
		` FROM audit`+w+` GROUP BY session_id ORDER BY MAX(at) DESC, session_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionStats
	for rows.Next() {
		var ss SessionStats
		var first, last int64
		if err := rows.Scan(append([]any{&ss.SessionID, &ss.AgentID, &ss.FleetID, &first, &last}, totalsDest(&ss.Totals)...)...); err != nil {
			return nil, err
		}
		ss.First, ss.Last = fromMS(first), fromMS(last)
		out = append(out, ss)
	}
	return out, rows.Err()
}

// FleetCostSince is the fleet's total cost from since on (everything if since
// is zero). The supervisor checks it against the fleet's budget.
func (s *Store) FleetCostSince(fleetID string, since time.Time) (float64, error) {
	var c float64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd),0) FROM audit WHERE fleet_id=? AND at>=?`, fleetID, ms(since)).Scan(&c)
	return c, err
}

// CostPoint is one bucket of the cost-over-time series.
type CostPoint struct {
	Start        time.Time
	CostUSD      float64
	OutputTokens int64
}

// CostSeries sums cost and output tokens into buckets of the given width,
// aligned to the unix epoch (so hourly buckets start on the hour in UTC).
// Empty buckets are left out; the chart fills the gaps. bucket must be at
// least a millisecond.
func (s *Store) CostSeries(f Filter, bucket time.Duration) ([]CostPoint, error) {
	b := bucket.Milliseconds()
	if b < 1 {
		b = time.Hour.Milliseconds()
	}
	w, args := f.where()
	args = append([]any{b, b}, args...)
	rows, err := s.db.Query(`SELECT (at / ?) * ? AS bucket, COALESCE(SUM(cost_usd),0), COALESCE(SUM(output),0)
		FROM audit`+w+` GROUP BY bucket ORDER BY bucket`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CostPoint
	for rows.Next() {
		var p CostPoint
		var start int64
		if err := rows.Scan(&start, &p.CostUSD, &p.OutputTokens); err != nil {
			return nil, err
		}
		p.Start = time.UnixMilli(start)
		out = append(out, p)
	}
	return out, rows.Err()
}
