package store

import (
	"errors"
	"strings"
	"time"

	"atlas-commander/internal/agent"
)

// AuditEntry is one row of the audit log. The log is append-only: the
// database refuses to change or delete a row.
//
// Tokens and CostUSD are summed by the analytics, so a unit of spend must be
// recorded on exactly one row (the supervisor puts it on the usage row).
type AuditEntry struct {
	Seq       int64 // set by the database
	At        time.Time
	FleetID   string
	AgentID   string
	SessionID string
	TaskID    string
	Kind      string // an agent.EventKind name, or "approval_decision"
	Tool      string
	Detail    string // JSON
	Usage     agent.Usage
	CostUSD   float64
	IsError   bool
}

const auditInsert = `INSERT INTO audit (at, fleet_id, agent_id, session_id, task_id, kind, tool, detail,
	input, output, cache_read, cache_write_5m, cache_write_1h, cost_usd, is_error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

func insertAudit(x execer, e *AuditEntry) error {
	if e.Kind == "" {
		return errors.New("an audit entry needs a kind")
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	res, err := x.Exec(auditInsert, ms(e.At), e.FleetID, e.AgentID, e.SessionID, e.TaskID, e.Kind, e.Tool, e.Detail,
		e.Usage.Input, e.Usage.Output, e.Usage.CacheRead, e.Usage.CacheWrite5m, e.Usage.CacheWrite1h, e.CostUSD, b2i(e.IsError))
	if err != nil {
		return err
	}
	e.Seq, _ = res.LastInsertId()
	return nil
}

// Append adds one entry and sets its Seq (and At, if it was zero).
func (s *Store) Append(e *AuditEntry) error { return insertAudit(s.db, e) }

// AppendBatch adds entries in one transaction: all or none. The supervisor
// batches so a burst of events costs one commit.
func (s *Store) AppendBatch(es []AuditEntry) error {
	if len(es) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range es {
		if err := insertAudit(tx, &es[i]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Filter narrows audit queries. Empty strings and zero times mean no limit;
// From is inclusive, To exclusive.
type Filter struct {
	FleetID   string
	AgentID   string
	SessionID string
	Kind      string
	From, To  time.Time
}

func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(c string, v any) { conds = append(conds, c); args = append(args, v) }
	if f.FleetID != "" {
		add("fleet_id=?", f.FleetID)
	}
	if f.AgentID != "" {
		add("agent_id=?", f.AgentID)
	}
	if f.SessionID != "" {
		add("session_id=?", f.SessionID)
	}
	if f.Kind != "" {
		add("kind=?", f.Kind)
	}
	if !f.From.IsZero() {
		add("at>=?", ms(f.From))
	}
	if !f.To.IsZero() {
		add("at<?", ms(f.To))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// AuditEntries returns matching entries, newest first. limit <= 0 means all.
func (s *Store) AuditEntries(f Filter, limit int) ([]AuditEntry, error) {
	w, args := f.where()
	q := `SELECT seq, at, fleet_id, agent_id, session_id, task_id, kind, tool, detail,
		input, output, cache_read, cache_write_5m, cache_write_1h, cost_usd, is_error FROM audit` + w + ` ORDER BY seq DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		var isErr int
		if err := rows.Scan(&e.Seq, &at, &e.FleetID, &e.AgentID, &e.SessionID, &e.TaskID, &e.Kind, &e.Tool, &e.Detail,
			&e.Usage.Input, &e.Usage.Output, &e.Usage.CacheRead, &e.Usage.CacheWrite5m, &e.Usage.CacheWrite1h, &e.CostUSD, &isErr); err != nil {
			return nil, err
		}
		e.At, e.IsError = fromMS(at), isErr != 0
		out = append(out, e)
	}
	return out, rows.Err()
}
