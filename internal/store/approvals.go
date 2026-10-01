package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Approval decisions.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Approval is a gate request: an agent wants to use a tool and a human must
// say yes or no. Pending ones (Decision == "") survive a restart so the
// board can still show them; the hook that asked is gone by then, so the
// supervisor should resolve them as denied.
type Approval struct {
	ID          string
	AgentID     string
	SessionID   string
	Tool        string
	Input       string // the tool input, JSON
	RequestedAt time.Time
	DecidedAt   time.Time
	Decision    string // "", "allow" or "deny"
	Reason      string
}

// AddApproval inserts a pending request, filling in ID and RequestedAt.
func (s *Store) AddApproval(a *Approval) error {
	if a.ID == "" {
		a.ID = NewID()
	}
	if a.RequestedAt.IsZero() {
		a.RequestedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO approvals (id, agent_id, session_id, tool, input, requested_at) VALUES (?,?,?,?,?,?)`,
		a.ID, a.AgentID, a.SessionID, a.Tool, a.Input, ms(a.RequestedAt))
	return mapErr(err, "approval "+a.ID)
}

// DecideApproval records the decision and writes it to the audit log in the
// same transaction, so the log never lacks a decision the board shows. It
// fails with ErrNotFound if the request does not exist or was already decided.
func (s *Store) DecideApproval(id, decision, reason string) error {
	if decision != DecisionAllow && decision != DecisionDeny {
		return fmt.Errorf("decision %q is not allow or deny", decision)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := decideTx(tx, id, decision, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// decideTx is DecideApproval's body, so DeleteAgent can deny inside its own
// transaction.
func decideTx(tx execer, id, decision, reason string) error {
	now := time.Now()
	res, err := tx.Exec(`UPDATE approvals SET decision=?, reason=?, decided_at=? WHERE id=? AND decision IS NULL`,
		decision, reason, ms(now), id)
	if err := needOne(res, err, "approval "+id); err != nil {
		return err
	}
	var a Approval
	if err := tx.QueryRow(`SELECT agent_id, session_id, tool FROM approvals WHERE id=?`, id).Scan(&a.AgentID, &a.SessionID, &a.Tool); err != nil {
		return err
	}
	var fleetID string
	if err := tx.QueryRow(`SELECT fleet_id FROM agents WHERE id=?`, a.AgentID).Scan(&fleetID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	detail, _ := json.Marshal(map[string]string{"approval_id": id, "decision": decision, "reason": reason})
	e := AuditEntry{At: now, FleetID: fleetID, AgentID: a.AgentID, SessionID: a.SessionID,
		Kind: "approval_decision", Tool: a.Tool, Detail: string(detail), IsError: decision == DecisionDeny}
	if err := insertAudit(tx, &e); err != nil {
		return err
	}
	return nil
}

// denyPending denies, with the audit row, every undecided approval matching
// the where clause (on the approvals table), before the agents go away and take their fleet id
// with them.
func denyPending(tx execer, where string, args ...any) error {
	rows, err := tx.Query(`SELECT id FROM approvals WHERE decision IS NULL AND `+where, args...)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := decideTx(tx, id, DecisionDeny, "agent removed"); err != nil {
			return err
		}
	}
	return nil
}

func scanApproval(r interface{ Scan(...any) error }) (Approval, error) {
	var a Approval
	var req, dec int64
	var decision sql.NullString
	err := r.Scan(&a.ID, &a.AgentID, &a.SessionID, &a.Tool, &a.Input, &req, &dec, &decision, &a.Reason)
	a.RequestedAt, a.DecidedAt, a.Decision = fromMS(req), fromMS(dec), decision.String
	return a, err
}

const approvalCols = `id, agent_id, session_id, tool, input, requested_at, decided_at, decision, reason`

// GetApproval returns one request.
func (s *Store) GetApproval(id string) (Approval, error) {
	a, err := scanApproval(s.db.QueryRow(`SELECT `+approvalCols+` FROM approvals WHERE id=?`, id))
	return a, mapErr(err, "approval "+id)
}

// PendingApprovals returns undecided requests whose agent still exists, oldest
// first. A request for a deleted agent can never be acted on.
func (s *Store) PendingApprovals() ([]Approval, error) {
	rows, err := s.db.Query(`SELECT ` + approvalCols + ` FROM approvals WHERE decision IS NULL AND agent_id IN (SELECT id FROM agents) ORDER BY requested_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
