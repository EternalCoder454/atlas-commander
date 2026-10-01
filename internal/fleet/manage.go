package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/store"
	"atlas-commander/internal/worktree"
)

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func (s *Supervisor) validateFleet(c FleetConfig, selfID string) (FleetConfig, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return c, errors.New("A fleet needs a name.")
	}
	if c.BudgetUSD < 0 {
		return c, errors.New("The budget can't be negative.")
	}
	if c.WorkDir == "" {
		c.WorkDir, _ = os.UserHomeDir()
	}
	if !isDir(c.WorkDir) {
		return c, fmt.Errorf("The folder %s doesn't exist.", c.WorkDir)
	}
	for _, f := range s.fleets {
		if f.ID != selfID && strings.EqualFold(f.Name, c.Name) {
			return c, fmt.Errorf("A fleet named %s already exists.", c.Name)
		}
	}
	return c, nil
}

// CreateFleet adds a fleet.
func (s *Supervisor) CreateFleet(c FleetConfig) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.validateFleet(c, "")
	if err != nil {
		return "", err
	}
	f := store.Fleet{Name: c.Name, Workdir: c.WorkDir, BudgetUSD: c.BudgetUSD}
	if err := s.st.CreateFleet(&f); err != nil {
		return "", storeErr(err, "Couldn't create the fleet.")
	}
	s.fleets = append(s.fleets, &f)
	s.touch()
	return f.ID, nil
}

// UpdateFleet changes a fleet's name, folder and budget.
func (s *Supervisor) UpdateFleet(id string, c FleetConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fleetByID(id)
	if f == nil {
		return errors.New("That fleet no longer exists.")
	}
	c, err := s.validateFleet(c, id)
	if err != nil {
		return err
	}
	n := *f
	n.Name, n.Workdir, n.BudgetUSD = c.Name, c.WorkDir, c.BudgetUSD
	if err := s.st.UpdateFleet(n); err != nil {
		return storeErr(err, "Couldn't save the fleet.")
	}
	*f = n
	s.touch()
	return nil
}

// DeleteFleet removes a fleet and its agents and tasks. The audit log stays.
// The agents' worktrees are removed afterwards, off the lock; one with
// uncommitted work is kept and named in a Notice.
func (s *Supervisor) DeleteFleet(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fleetByID(id)
	if f == nil {
		return errors.New("That fleet no longer exists.")
	}
	for _, a := range s.list {
		if a.cfg.FleetID == id && (a.status.Live() || a.sess != nil || a.starting) {
			return errors.New("Stop this fleet's agents before deleting it.")
		}
	}
	var trees []treeJob
	for _, a := range s.list {
		if a.cfg.FleetID == id && a.cfg.WorktreePath != "" {
			trees = append(trees, treeJob{name: a.cfg.Name, repo: s.effectiveDir(a), path: a.cfg.WorktreePath, branch: a.cfg.Branch})
		}
	}
	if err := s.st.DeleteFleet(id); err != nil {
		return storeErr(err, "Couldn't delete the fleet.")
	}
	s.fleets = slices.DeleteFunc(s.fleets, func(x *store.Fleet) bool { return x.ID == id })
	s.list = slices.DeleteFunc(s.list, func(a *agentState) bool {
		if a.cfg.FleetID == id {
			delete(s.agents, a.cfg.ID)
			return true
		}
		return false
	})
	s.touchTasks()
	if len(trees) > 0 {
		s.spawnLocked(func() {
			var fx effects
			for _, j := range trees {
				if err := s.removeTree(j, true); err != nil {
					fx.notes = append(fx.notes, Notice{Title: "Kept " + j.name + "'s worktree", Body: treeKeptText(err, j.path), Error: true})
				}
			}
			s.emit(fx.notes)
		})
	}
	return nil
}

// treeJob is one worktree to remove, captured under the lock.
type treeJob struct {
	name, repo, path, branch string
}

// removeTree deletes the worktree and, with dropBranch, its branch if fully
// merged. It takes no supervisor lock, since git can be slow.
func (s *Supervisor) removeTree(j treeJob, dropBranch bool) error {
	if err := worktree.Remove(j.repo, s.o.WorktreeRoot, j.path); err != nil {
		return err
	}
	if dropBranch && j.branch != "" {
		_ = worktree.RemoveBranch(j.repo, j.branch) // unmerged work keeps its branch
	}
	return nil
}

func treeKeptText(err error, path string) string {
	if errors.Is(err, worktree.ErrDirty) {
		return "It has changes that aren't committed, so it was left at " + path + "."
	}
	return truncate(err.Error(), 200)
}

// storeErr turns a store error into a plain sentence.
func storeErr(err error, fallback string) error {
	switch {
	case errors.Is(err, store.ErrExists):
		return errors.New("That name is already taken.")
	case errors.Is(err, store.ErrNotFound):
		return errors.New("That item no longer exists.")
	case errors.Is(err, store.ErrConflict):
		return errors.New("That isn't allowed in its current state.")
	}
	return errors.New(fallback)
}

func (s *Supervisor) knownBackend(name string) bool {
	if name == agent.BackendClaudeCode || name == agent.BackendClaudeAPI {
		return true
	}
	_, ok := s.o.Backends[name]
	return ok
}

func (s *Supervisor) validateAgent(c AgentConfig, selfID string) (AgentConfig, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Model = strings.TrimSpace(c.Model)
	f := s.fleetByID(c.FleetID)
	switch {
	case f == nil:
		return c, errors.New("That fleet no longer exists.")
	case c.Name == "":
		return c, errors.New("An agent needs a name.")
	case !s.knownBackend(c.Backend):
		return c, fmt.Errorf("%q isn't a known backend.", c.Backend)
	case c.Model == "":
		return c, errors.New("An agent needs a model.")
	case c.CostCapUSD < 0:
		return c, errors.New("The cost cap can't be negative.")
	}
	dir := c.WorkDir
	if dir == "" {
		dir = f.Workdir
	}
	if !isDir(dir) {
		return c, fmt.Errorf("The folder %s doesn't exist.", dir)
	}
	for _, a := range s.list {
		if a.cfg.FleetID == c.FleetID && a.cfg.ID != selfID && strings.EqualFold(a.cfg.Name, c.Name) {
			return c, fmt.Errorf("This fleet already has an agent named %s.", c.Name)
		}
	}
	return c, nil
}

func policyJSON(approve []string) string {
	if approve == nil {
		approve = []string{}
	}
	b, _ := json.Marshal(map[string][]string{"approve": approve})
	return string(b)
}

// RegisterAgent adds an agent to a fleet.
func (s *Supervisor) RegisterAgent(c AgentConfig) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.validateAgent(c, "")
	if err != nil {
		return "", err
	}
	ca := store.Agent{
		FleetID: c.FleetID, Name: c.Name, Backend: c.Backend, Model: c.Model, Workdir: c.WorkDir,
		PinnedPrompt: c.PinnedPrompt, CostCapUSD: c.CostCapUSD, UseWorktree: c.UseWorktree,
		GatePolicy: policyJSON(c.Approve), Status: string(StatusIdle),
	}
	if err := s.st.CreateAgent(&ca); err != nil {
		if errors.Is(err, store.ErrExists) {
			return "", fmt.Errorf("This fleet already has an agent named %s.", c.Name)
		}
		return "", storeErr(err, "Couldn't save the agent.")
	}
	a := newAgentState(ca)
	s.agents[ca.ID] = a
	s.list = append(s.list, a)
	s.touch()
	return ca.ID, nil
}

func configOf(ca store.Agent) AgentConfig {
	return AgentConfig{
		FleetID: ca.FleetID, Name: ca.Name, Backend: ca.Backend, Model: ca.Model, WorkDir: ca.Workdir,
		PinnedPrompt: ca.PinnedPrompt, CostCapUSD: ca.CostCapUSD, UseWorktree: ca.UseWorktree,
		Approve: append([]string{}, parseApprove(ca.GatePolicy)...),
	}
}

// AgentConfig returns the stored configuration.
func (s *Supervisor) AgentConfig(id string) (AgentConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[id]
	if a == nil {
		return AgentConfig{}, errors.New("That agent no longer exists.")
	}
	return configOf(a.cfg), nil
}

// UpdateAgent saves new settings. While the agent is live only the cost cap
// and the approval list may change, since the rest is baked into the running
// session.
func (s *Supervisor) UpdateAgent(id string, c AgentConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[id]
	if a == nil {
		return errors.New("That agent no longer exists.")
	}
	if c.FleetID == "" {
		c.FleetID = a.cfg.FleetID
	}
	if c.FleetID != a.cfg.FleetID {
		return errors.New("An agent can't move to another fleet.")
	}
	c, err := s.validateAgent(c, id)
	if err != nil {
		return err
	}
	if a.status.Live() || a.sess != nil {
		o := a.cfg
		if o.Name != c.Name || o.Backend != c.Backend || o.Model != c.Model || o.Workdir != c.WorkDir ||
			o.PinnedPrompt != c.PinnedPrompt || o.UseWorktree != c.UseWorktree {
			return errors.New("Stop this agent before changing anything but its cost cap or approvals.")
		}
	}
	n := a.cfg
	n.Name, n.Backend, n.Model, n.Workdir = c.Name, c.Backend, c.Model, c.WorkDir
	n.PinnedPrompt, n.CostCapUSD, n.UseWorktree = c.PinnedPrompt, c.CostCapUSD, c.UseWorktree
	n.GatePolicy = policyJSON(c.Approve)
	if err := s.st.UpdateAgent(n); err != nil {
		return storeErr(err, "Couldn't save the agent.")
	}
	a.cfg = n
	a.approve = parseApprove(n.GatePolicy)
	s.touch()
	return nil
}

// ArchiveAgent hides an agent and removes its worktree. The agent is archived
// at once, so it can't start; git then runs off the lock. A worktree with
// uncommitted work is kept, and a Notice says so.
func (s *Supervisor) ArchiveAgent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[id]
	if a == nil {
		return errors.New("That agent no longer exists.")
	}
	if a.status.Live() || a.sess != nil || a.starting {
		return errors.New("Stop this agent before archiving it.")
	}
	n := a.cfg
	n.Archived = true
	if err := s.st.UpdateAgent(n); err != nil {
		return storeErr(err, "Couldn't archive the agent.")
	}
	a.cfg = n
	s.touch()
	if n.WorktreePath == "" {
		return nil
	}
	j := treeJob{name: n.Name, repo: s.effectiveDir(a), path: n.WorktreePath}
	s.spawnLocked(func() {
		err := s.removeTree(j, false)
		s.mu.Lock()
		if err == nil && a.cfg.WorktreePath == j.path {
			a.cfg.WorktreePath, a.cfg.Branch = "", ""
			_ = s.st.UpdateAgent(a.cfg)
			s.touch()
		}
		s.mu.Unlock()
		if err != nil {
			// Any failure keeps the tree, so no work is lost.
			s.emit([]Notice{{Title: "Kept " + j.name + "'s worktree", Body: treeKeptText(err, j.path), AgentID: id, Error: true}})
		}
	})
	return nil
}

// ---- analytics: straight to the store ----

func (s *Supervisor) Totals(f store.Filter) (store.Totals, error) { return s.st.FleetTotals(f) }

func (s *Supervisor) AgentStats(f store.Filter) ([]store.AgentStats, error) {
	return s.st.AgentStats(f)
}

func (s *Supervisor) SessionStats(f store.Filter) ([]store.SessionStats, error) {
	return s.st.SessionStats(f)
}

func (s *Supervisor) CostSeries(f store.Filter, bucket time.Duration) ([]store.CostPoint, error) {
	return s.st.CostSeries(f, bucket)
}

func (s *Supervisor) Audit(f store.Filter, limit int) ([]store.AuditEntry, error) {
	return s.st.AuditEntries(f, limit)
}
