package fleet

import (
	"errors"
	"slices"
	"strings"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/store"
)

// depError makes a store dependency error a plain sentence.
func depError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("A task it depends on no longer exists.")
	}
	msg := err.Error()
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return errors.New(capitalize(msg))
}

// AddTask creates a queued task with its dependencies.
func (s *Supervisor) AddTask(c TaskConfig) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fleetByID(c.FleetID) == nil {
		return "", errors.New("That fleet no longer exists.")
	}
	title := strings.TrimSpace(c.Title)
	if title == "" {
		return "", errors.New("A task needs a title.")
	}
	prompt := c.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = title
	}
	t := store.Task{FleetID: c.FleetID, Title: title, Prompt: prompt, Priority: c.Priority}
	if err := s.st.CreateTask(&t); err != nil {
		return "", storeErr(err, "Couldn't save the task.")
	}
	for _, d := range c.DependsOn {
		if err := s.st.AddTaskDep(t.ID, d); err != nil {
			_ = s.st.DeleteTask(t.ID)
			return "", depError(err)
		}
	}
	s.touchTasks()
	return t.ID, nil
}

// UpdateTask edits a queued task, including what it depends on.
func (s *Supervisor) UpdateTask(id string, c TaskConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.st.GetTask(id)
	if err != nil {
		return storeErr(err, "Couldn't read the task.")
	}
	if t.Status != store.TaskQueued {
		return errors.New("Only a queued task can be edited.")
	}
	title := strings.TrimSpace(c.Title)
	if title == "" {
		return errors.New("A task needs a title.")
	}
	if strings.TrimSpace(c.Prompt) == "" {
		c.Prompt = title
	}
	old, err := s.st.TaskDeps(id)
	if err != nil {
		return storeErr(err, "Couldn't read the task.")
	}
	var added []string
	for _, d := range c.DependsOn {
		if slices.Contains(old, d) {
			continue
		}
		if err := s.st.AddTaskDep(id, d); err != nil {
			for _, x := range added {
				_ = s.st.RemoveTaskDep(id, x)
			}
			return depError(err)
		}
		added = append(added, d)
	}
	for _, d := range old {
		if !slices.Contains(c.DependsOn, d) {
			_ = s.st.RemoveTaskDep(id, d)
		}
	}
	t.Title, t.Prompt, t.Priority = title, c.Prompt, c.Priority
	if err := s.st.UpdateTask(t); err != nil {
		return storeErr(err, "Couldn't save the task.")
	}
	s.touchTasks()
	return nil
}

// CancelTask cancels a queued or running task. A running agent keeps going;
// only the task's record changes.
func (s *Supervisor) CancelTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.st.SetTaskStatus(id, store.TaskCancelled, ""); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errors.New("This task has already finished.")
		}
		return storeErr(err, "Couldn't cancel the task.")
	}
	for _, a := range s.list {
		if a.taskID == id {
			a.taskID = ""
			s.touch()
		}
	}
	s.touchTasks()
	return nil
}

// Dispatch hands a queued, ready task to an agent of the same fleet.
func (s *Supervisor) Dispatch(taskID, agentID string) error {
	t, err := s.st.GetTask(taskID)
	if err != nil {
		return storeErr(err, "Couldn't read the task.")
	}
	if t.Status != store.TaskQueued {
		return errors.New("Only a queued task can be dispatched.")
	}
	ready, err := s.st.ReadyTasks(t.FleetID)
	if err != nil {
		return storeErr(err, "Couldn't read the task.")
	}
	if !slices.ContainsFunc(ready, func(r store.Task) bool { return r.ID == taskID }) {
		return errors.New("This task is waiting for tasks it depends on.")
	}
	s.mu.Lock()
	a := s.agents[agentID]
	switch {
	case a == nil:
		s.mu.Unlock()
		return errors.New("That agent no longer exists.")
	case a.cfg.FleetID != t.FleetID:
		s.mu.Unlock()
		return errors.New("That agent belongs to a different fleet.")
	case a.cfg.Archived:
		s.mu.Unlock()
		return errors.New("This agent is archived.")
	case a.base == StatusCapped:
		s.mu.Unlock()
		return errors.New("This agent has reached its cost cap.")
	}
	switch a.status {
	case StatusIdle, StatusStopped, StatusError:
		s.mu.Unlock()
		return s.startAgent(agentID, t.Prompt, &t)
	case StatusWaiting:
		defer s.mu.Unlock()
		if a.outbox == nil || len(a.outbox) == cap(a.outbox) {
			return errors.New("Too many messages are waiting for this agent. Try again in a moment.")
		}
		if err := s.claimTask(&t, a); err != nil {
			return err
		}
		s.pushAudit(s.row(a, "task_dispatch", "", map[string]any{"task_id": t.ID, "title": t.Title}))
		return s.sendLocked(a, t.Prompt)
	}
	s.mu.Unlock()
	return errors.New("This agent is busy. Wait for it to finish or stop it first.")
}

// claimTask marks the task running on agent a. Call with mu held. The store
// refuses it if the task was taken or cancelled since it was read.
func (s *Supervisor) claimTask(t *store.Task, a *agentState) error {
	if err := s.st.SetTaskStatus(t.ID, store.TaskRunning, ""); err != nil {
		return errors.New("That task is no longer queued.")
	}
	n := *t
	n.AgentID = a.cfg.ID
	_ = s.st.UpdateTask(n)
	a.task, a.taskID = t.Title, t.ID
	s.touchTasks()
	return nil
}

// finishTask closes the agent's task when a turn ends. Call with mu held.
func (s *Supervisor) finishTask(a *agentState, ev agent.Event) {
	if a.taskID == "" {
		return
	}
	status := store.TaskDone
	if ev.IsError {
		status = store.TaskFailed
	}
	// A conflict means the user cancelled it meanwhile; that stands.
	_ = s.st.SetTaskStatus(a.taskID, status, truncate(ev.Text, maxDetailText))
	a.taskID = ""
	s.touchTasks()
}
