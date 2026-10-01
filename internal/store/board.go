package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Fleet is a named group of agents with a working directory and a budget.
type Fleet struct {
	ID        string
	Name      string
	Workdir   string
	BudgetUSD float64 // 0 means no budget
	CreatedAt time.Time
}

// Agent is one configured worker. SessionID is the backend session to resume.
type Agent struct {
	ID           string
	FleetID      string
	Name         string
	Backend      string
	Model        string
	Workdir      string
	PinnedPrompt string
	CostCapUSD   float64 // 0 means no cap
	UseWorktree  bool
	WorktreePath string
	Branch       string
	GatePolicy   string // JSON: which tools need approval
	SessionID    string
	Status       string
	Archived     bool
	CreatedAt    time.Time
}

// Task statuses.
const (
	TaskQueued    = "queued"
	TaskRunning   = "running"
	TaskDone      = "done"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

func validTaskStatus(s string) bool {
	switch s {
	case TaskQueued, TaskRunning, TaskDone, TaskFailed, TaskCancelled:
		return true
	}
	return false
}

// Task is one unit of work on the board. AgentID is "" until assigned, and
// goes back to "" if the agent is deleted.
type Task struct {
	ID         string
	FleetID    string
	AgentID    string
	Title      string
	Prompt     string
	Priority   int
	Status     string
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Result     string
}

// ---- fleets ----

// CreateFleet inserts f, filling in ID and CreatedAt when empty.
func (s *Store) CreateFleet(f *Fleet) error {
	if f.ID == "" {
		f.ID = NewID()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO fleets (id, name, workdir, budget_usd, created_at) VALUES (?,?,?,?,?)`,
		f.ID, f.Name, f.Workdir, f.BudgetUSD, ms(f.CreatedAt))
	return mapErr(err, "fleet "+f.Name)
}

// UpdateFleet saves every field but ID and CreatedAt.
func (s *Store) UpdateFleet(f Fleet) error {
	res, err := s.db.Exec(`UPDATE fleets SET name=?, workdir=?, budget_usd=? WHERE id=?`, f.Name, f.Workdir, f.BudgetUSD, f.ID)
	return needOne(res, err, "fleet "+f.ID)
}

const fleetCols = `id, name, workdir, budget_usd, created_at`

func scanFleet(r interface{ Scan(...any) error }) (Fleet, error) {
	var f Fleet
	var at int64
	err := r.Scan(&f.ID, &f.Name, &f.Workdir, &f.BudgetUSD, &at)
	f.CreatedAt = fromMS(at)
	return f, err
}

// GetFleet returns the fleet with the given id.
func (s *Store) GetFleet(id string) (Fleet, error) {
	f, err := scanFleet(s.db.QueryRow(`SELECT `+fleetCols+` FROM fleets WHERE id=?`, id))
	return f, mapErr(err, "fleet "+id)
}

// FleetByName returns the fleet with the given name.
func (s *Store) FleetByName(name string) (Fleet, error) {
	f, err := scanFleet(s.db.QueryRow(`SELECT `+fleetCols+` FROM fleets WHERE name=?`, name))
	return f, mapErr(err, "fleet "+name)
}

// ListFleets returns all fleets, oldest first.
func (s *Store) ListFleets() ([]Fleet, error) {
	rows, err := s.db.Query(`SELECT ` + fleetCols + ` FROM fleets ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fleet
	for rows.Next() {
		f, err := scanFleet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFleet removes the fleet with its agents and tasks. Its audit rows stay.
// Approvals still pending for its agents are denied first.
func (s *Store) DeleteFleet(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := denyPending(tx, `agent_id IN (SELECT id FROM agents WHERE fleet_id=?)`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM fleets WHERE id=?`, id)
	if err := needOne(res, err, "fleet "+id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- agents ----

// CreateAgent inserts a, filling in ID, CreatedAt and the default gate policy.
func (s *Store) CreateAgent(a *Agent) error {
	if a.ID == "" {
		a.ID = NewID()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	if a.GatePolicy == "" {
		a.GatePolicy = DefaultGatePolicy
	}
	_, err := s.db.Exec(`INSERT INTO agents (id, fleet_id, name, backend, model, workdir, pinned_prompt, cost_cap_usd,
		use_worktree, worktree_path, branch, gate_policy, session_id, status, archived, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.FleetID, a.Name, a.Backend, a.Model, a.Workdir, a.PinnedPrompt, a.CostCapUSD,
		b2i(a.UseWorktree), a.WorktreePath, a.Branch, a.GatePolicy, a.SessionID, a.Status, b2i(a.Archived), ms(a.CreatedAt))
	return mapErr(err, "agent "+a.Name)
}

// UpdateAgent saves every field but ID, FleetID and CreatedAt.
func (s *Store) UpdateAgent(a Agent) error {
	if a.GatePolicy == "" {
		a.GatePolicy = DefaultGatePolicy
	}
	res, err := s.db.Exec(`UPDATE agents SET name=?, backend=?, model=?, workdir=?, pinned_prompt=?, cost_cap_usd=?,
		use_worktree=?, worktree_path=?, branch=?, gate_policy=?, session_id=?, status=?, archived=? WHERE id=?`,
		a.Name, a.Backend, a.Model, a.Workdir, a.PinnedPrompt, a.CostCapUSD,
		b2i(a.UseWorktree), a.WorktreePath, a.Branch, a.GatePolicy, a.SessionID, a.Status, b2i(a.Archived), a.ID)
	return needOne(res, err, "agent "+a.ID)
}

// SetAgentStatus changes only the status, which changes often.
func (s *Store) SetAgentStatus(id, status string) error {
	res, err := s.db.Exec(`UPDATE agents SET status=? WHERE id=?`, status, id)
	return needOne(res, err, "agent "+id)
}

// SetAgentSession records the backend session to resume after a restart.
func (s *Store) SetAgentSession(id, sessionID string) error {
	res, err := s.db.Exec(`UPDATE agents SET session_id=? WHERE id=?`, sessionID, id)
	return needOne(res, err, "agent "+id)
}

const agentCols = `id, fleet_id, name, backend, model, workdir, pinned_prompt, cost_cap_usd, use_worktree,
	worktree_path, branch, gate_policy, session_id, status, archived, created_at`

func scanAgent(r interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var wt, arch int
	var at int64
	err := r.Scan(&a.ID, &a.FleetID, &a.Name, &a.Backend, &a.Model, &a.Workdir, &a.PinnedPrompt, &a.CostCapUSD,
		&wt, &a.WorktreePath, &a.Branch, &a.GatePolicy, &a.SessionID, &a.Status, &arch, &at)
	a.UseWorktree, a.Archived, a.CreatedAt = wt != 0, arch != 0, fromMS(at)
	return a, err
}

// GetAgent returns the agent with the given id.
func (s *Store) GetAgent(id string) (Agent, error) {
	a, err := scanAgent(s.db.QueryRow(`SELECT `+agentCols+` FROM agents WHERE id=?`, id))
	return a, mapErr(err, "agent "+id)
}

// ListAgents returns a fleet's agents, oldest first. Archived ones are left
// out unless includeArchived is set.
func (s *Store) ListAgents(fleetID string, includeArchived bool) ([]Agent, error) {
	q := `SELECT ` + agentCols + ` FROM agents WHERE fleet_id=?`
	if !includeArchived {
		q += ` AND archived=0`
	}
	rows, err := s.db.Query(q+` ORDER BY created_at, name`, fleetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAgent removes the agent. Its tasks stay, unassigned; its audit rows
// stay. Approvals it still has pending are denied with the reason "agent
// removed", so the hook that is waiting on one gets an answer.
func (s *Store) DeleteAgent(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := denyPending(tx, `agent_id=?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM agents WHERE id=?`, id)
	if err := needOne(res, err, "agent "+id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- tasks ----

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateTask inserts t, filling in ID, CreatedAt and the queued status.
func (s *Store) CreateTask(t *Task) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	if t.Status == "" {
		t.Status = TaskQueued
	}
	if !validTaskStatus(t.Status) {
		return fmt.Errorf("task status %q is not one of queued, running, done, failed, cancelled", t.Status)
	}
	_, err := s.db.Exec(`INSERT INTO tasks (id, fleet_id, agent_id, title, prompt, priority, status, created_at, started_at, finished_at, result)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.FleetID, nullStr(t.AgentID), t.Title, t.Prompt, t.Priority, t.Status, ms(t.CreatedAt), ms(t.StartedAt), ms(t.FinishedAt), t.Result)
	return mapErr(err, "task "+t.Title)
}

// UpdateTask saves the fields a user edits: title, prompt, priority and
// agent. Status, timestamps and result belong to SetTaskStatus, so an editor
// holding a stale copy cannot revive a task that finished meanwhile.
func (s *Store) UpdateTask(t Task) error {
	res, err := s.db.Exec(`UPDATE tasks SET agent_id=?, title=?, prompt=?, priority=? WHERE id=?`,
		nullStr(t.AgentID), t.Title, t.Prompt, t.Priority, t.ID)
	return needOne(res, err, "task "+t.ID)
}

// SetTaskStatus moves a task to status, stamping started_at when it starts
// running and finished_at when it ends. result is stored as given.
//
// Only queued -> running/done/failed/cancelled and running ->
// done/failed/cancelled are allowed. Done, failed and cancelled are final, so
// a late "done" from an agent whose task was cancelled gets ErrConflict
// instead of overwriting the cancel. The check is part of the UPDATE, so it
// holds against other processes too.
func (s *Store) SetTaskStatus(id, status, result string) error {
	if !validTaskStatus(status) {
		return fmt.Errorf("task status %q is not one of queued, running, done, failed, cancelled", status)
	}
	now := time.Now().UnixMilli()
	var res sql.Result
	var err error
	switch status {
	case TaskRunning:
		res, err = s.db.Exec(`UPDATE tasks SET status='running', started_at=CASE WHEN started_at=0 THEN ? ELSE started_at END, result=?
			WHERE id=? AND status='queued'`, now, result, id)
	case TaskQueued:
		// Nothing may move back to queued.
		return s.conflictOrMissing(id, "task "+id)
	default:
		res, err = s.db.Exec(`UPDATE tasks SET status=?, finished_at=?, result=? WHERE id=? AND status IN ('queued','running')`,
			status, now, result, id)
	}
	if err != nil {
		return mapErr(err, "task "+id)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.conflictOrMissing(id, "task "+id)
	}
	return nil
}

// conflictOrMissing says why a guarded UPDATE touched nothing.
func (s *Store) conflictOrMissing(id, what string) error {
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM tasks WHERE id=?`, id).Scan(&one); err != nil {
		return mapErr(err, what)
	}
	return fmt.Errorf("%s: %w", what, ErrConflict)
}

const taskCols = `id, fleet_id, agent_id, title, prompt, priority, status, created_at, started_at, finished_at, result`

func scanTask(r interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var agentID sql.NullString
	var c, st, fin int64
	err := r.Scan(&t.ID, &t.FleetID, &agentID, &t.Title, &t.Prompt, &t.Priority, &t.Status, &c, &st, &fin, &t.Result)
	t.AgentID, t.CreatedAt, t.StartedAt, t.FinishedAt = agentID.String, fromMS(c), fromMS(st), fromMS(fin)
	return t, err
}

// GetTask returns the task with the given id.
func (s *Store) GetTask(id string) (Task, error) {
	t, err := scanTask(s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
	return t, mapErr(err, "task "+id)
}

func (s *Store) queryTasks(q string, args ...any) ([]Task, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTasks returns a fleet's tasks, best first (priority, then oldest). An
// empty status means all of them.
func (s *Store) ListTasks(fleetID, status string) ([]Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE fleet_id=?`
	args := []any{fleetID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	return s.queryTasks(q+` ORDER BY priority DESC, created_at, id`, args...)
}

// DeleteTask removes the task; dependencies on it go too.
func (s *Store) DeleteTask(id string) error {
	res, err := s.db.Exec(`DELETE FROM tasks WHERE id=?`, id)
	return needOne(res, err, "task "+id)
}

// AddTaskDep makes taskID wait for dependsOn. A dependency that would make a
// cycle is refused, because neither task could ever become ready.
func (s *Store) AddTaskDep(taskID, dependsOn string) error {
	if taskID == dependsOn {
		return errors.New("a task cannot depend on itself")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fa, fb string
	if err := tx.QueryRow(`SELECT fleet_id FROM tasks WHERE id=?`, taskID).Scan(&fa); err != nil {
		return mapErr(err, "task "+taskID)
	}
	if err := tx.QueryRow(`SELECT fleet_id FROM tasks WHERE id=?`, dependsOn).Scan(&fb); err != nil {
		return mapErr(err, "task "+dependsOn)
	}
	if fa != fb {
		return errors.New("a task can only depend on a task in the same fleet")
	}
	// The new edge taskID -> dependsOn closes a cycle exactly when dependsOn
	// already reaches taskID through existing edges.
	var one int
	err = tx.QueryRow(`WITH RECURSIVE reach(id) AS (
			SELECT depends_on FROM task_deps WHERE task_id=?
			UNION
			SELECT d.depends_on FROM task_deps d JOIN reach r ON d.task_id=r.id
		) SELECT 1 FROM reach WHERE id=? LIMIT 1`, dependsOn, taskID).Scan(&one)
	if err == nil {
		return errors.New("that dependency would make a cycle")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO task_deps (task_id, depends_on) VALUES (?,?)`, taskID, dependsOn); err != nil {
		if isFK(err) {
			return fmt.Errorf("task dependency: %w", ErrNotFound)
		}
		return err
	}
	return tx.Commit()
}

// RemoveTaskDep drops one dependency; removing one that is not there is fine.
func (s *Store) RemoveTaskDep(taskID, dependsOn string) error {
	_, err := s.db.Exec(`DELETE FROM task_deps WHERE task_id=? AND depends_on=?`, taskID, dependsOn)
	return err
}

// TaskDeps returns the ids taskID waits for.
func (s *Store) TaskDeps(taskID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT depends_on FROM task_deps WHERE task_id=? ORDER BY depends_on`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReadyTasks returns the fleet's queued tasks whose dependencies are all done,
// highest priority first, then oldest. A dependency that failed or was
// cancelled keeps its dependents waiting; the user decides what to do.
func (s *Store) ReadyTasks(fleetID string) ([]Task, error) {
	return s.queryTasks(`SELECT `+taskCols+` FROM tasks t WHERE t.fleet_id=? AND t.status='queued'
		AND NOT EXISTS (SELECT 1 FROM task_deps d JOIN tasks p ON p.id=d.depends_on WHERE d.task_id=t.id AND p.status<>'done')
		ORDER BY t.priority DESC, t.created_at, t.id`, fleetID)
}
