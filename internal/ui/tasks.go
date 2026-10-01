package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
)

// tasksPage is the work queue. Tasks are dispatched by hand: pick a ready
// task, pick an agent in its fleet. Dependencies only gate readiness.
type tasksPage struct {
	app *App
	w   *qt.QWidget
	seq uint64
	// seeded is set once the first snapshot has been taken; an empty task
	// list is a valid state, so rows != nil can't say it.
	seeded bool
	now    time.Time
	snap   *fleet.Snapshot
	rows   []fleet.TaskView
	byID   map[string]*fleet.TaskView

	summary *liveLabel
	list    *listView
	table   *dataTable
	btn     struct {
		add, edit, dispatch, cancel *qt.QPushButton
	}
	menu *qt.QMenu
}

var taskCols = []tableCol{
	{title: "Status", width: 124},
	{title: "Priority", width: 74, mono: true, right: true},
	{title: "Task"},
	{title: "Fleet", width: 120},
	{title: "Waits on", width: 180},
	{title: "Agent", width: 150},
	{title: "Added", width: 84, mono: true, right: true},
}

func newTasksPage(a *App) *tasksPage {
	p := &tasksPage{app: a, w: qt.NewQWidget2()}
	l := newPageLayout(p.w)

	top := qt.NewQHBoxLayout2()
	top.SetSpacing(12)
	top.AddWidget(pageTitle("Tasks").QWidget)
	p.summary = newLiveLabel("")
	setProp(p.summary.L.QWidget, "caption", true)
	top.AddWidget(p.summary.L.QWidget)
	top.AddStretch()
	l.AddLayout(top.QLayout)

	row := qt.NewQHBoxLayout2()
	row.SetSpacing(4)
	p.btn.add = qt.NewQPushButton3("New task")
	setProp(p.btn.add.QWidget, "accent", true)
	p.btn.add.OnClicked(func() { p.editTask("") })
	row.AddWidget(p.btn.add.QWidget)
	p.btn.edit = qt.NewQPushButton3("Edit")
	p.btn.edit.SetToolTip("Change a queued task")
	p.btn.edit.OnClicked(func() { p.editTask(p.table.selected) })
	row.AddWidget(p.btn.edit.QWidget)
	p.btn.dispatch = qt.NewQPushButton3("Dispatch to…")
	p.btn.dispatch.SetToolTip("Send this task to an agent in its fleet")
	setProp(p.btn.dispatch.QWidget, "menu", true)
	p.menu = qt.NewQMenu(p.btn.dispatch.QWidget)
	p.menu.OnAboutToShow(p.fillDispatchMenu)
	p.btn.dispatch.SetMenu(p.menu)
	row.AddWidget(p.btn.dispatch.QWidget)
	row.AddStretch()
	p.btn.cancel = qt.NewQPushButton3("Cancel task")
	setProp(p.btn.cancel.QWidget, "danger", true)
	p.btn.cancel.SetToolTip("Take the task off the queue. A running agent is not stopped.")
	p.btn.cancel.OnClicked(p.cancelTask)
	row.AddWidget(p.btn.cancel.QWidget)
	l.AddLayout(row.QLayout)

	p.table = newDataTable(a, taskCols, p.cell)
	p.table.tip = p.tip
	p.table.onSelect = func(string) { p.updateButtons() }
	p.table.onActivate = func(id string) { p.editTask(id) }
	p.table.onSort = p.resort
	p.list = newListView(p.table, "No tasks yet",
		"Tasks are jobs waiting for an agent. Choose New task to add one, then send it to an agent in its fleet.")
	l.AddWidget(p.list.W.QWidget)
	p.updateButtons()
	return p
}

func (p *tasksPage) widget() *qt.QWidget { return p.w }

func taskTone(t *fleet.TaskView) fleet.Tone {
	switch {
	case t.Status == "running":
		return fleet.ToneOK
	case t.Status == "failed":
		return fleet.ToneError
	case t.Status == "queued" && t.Ready:
		return fleet.ToneWarn
	}
	return fleet.ToneIdle
}

func taskStatusText(t *fleet.TaskView) string {
	switch t.Status {
	case "queued":
		if t.Ready {
			return "Ready"
		}
		return "Blocked"
	case "running":
		return "Running"
	case "done":
		return "Done"
	case "failed":
		return "Failed"
	}
	return t.Status
}

func taskRank(t *fleet.TaskView) int {
	switch {
	case t.Status == "running":
		return 0
	case t.Status == "queued" && t.Ready:
		return 1
	case t.Status == "queued":
		return 2
	case t.Status == "failed":
		return 3
	}
	return 4
}

func (p *tasksPage) fleetName(id string) string {
	if p.snap != nil {
		for _, f := range p.snap.Fleets {
			if f.ID == id {
				return f.Name
			}
		}
	}
	return ""
}

func (p *tasksPage) deps(t *fleet.TaskView) string {
	var names []string
	for _, d := range t.DependsOn {
		if o := p.byID[d]; o != nil {
			if o.Status != "done" {
				names = append(names, o.Title)
			}
		}
	}
	return strings.Join(names, ", ")
}

func (p *tasksPage) cell(r, c int) cell {
	t := &p.rows[r]
	done := t.Status == "done"
	dim := 0.0
	if done {
		dim = 0.6
	}
	switch c {
	case 0:
		return cell{text: taskStatusText(t), dot: true, dotTone: taskTone(t)}
	case 1:
		return cell{text: fmt.Sprint(t.Priority), alpha: dim}
	case 2:
		return cell{text: t.Title, alpha: dim}
	case 3:
		return cell{text: p.fleetName(t.FleetID), alpha: 0.75}
	case 4:
		return cell{text: p.deps(t), alpha: 0.75}
	case 5:
		return cell{text: t.AgentName, alpha: dim}
	case 6:
		return cell{text: fmtAgo(t.Created, p.now), alpha: 0.75}
	}
	return cell{}
}

func (p *tasksPage) tip(r, c int) string {
	t := &p.rows[r]
	switch c {
	case 2:
		if t.Prompt != "" {
			return t.Prompt
		}
	case 4:
		var all []string
		for _, d := range t.DependsOn {
			if o := p.byID[d]; o != nil {
				all = append(all, fmt.Sprintf("%s (%s)", o.Title, strings.ToLower(taskStatusText(o))))
			}
		}
		return strings.Join(all, "\n")
	}
	return ""
}

func (p *tasksPage) refresh(s *fleet.Snapshot) {
	p.now = time.Now()
	if s.Seq == p.seq && p.seeded {
		p.table.T.Viewport().Update()
		return
	}
	p.seq, p.snap, p.seeded = s.Seq, s, true
	p.rows = make([]fleet.TaskView, len(s.Tasks))
	copy(p.rows, s.Tasks)
	ready, running, blocked := 0, 0, 0
	for _, t := range p.rows {
		switch {
		case t.Status == "running":
			running++
		case t.Status == "queued" && t.Ready:
			ready++
		case t.Status == "queued":
			blocked++
		}
	}
	var parts []string
	if ready > 0 {
		parts = append(parts, fmt.Sprintf("%d ready", ready))
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	p.summary.Set(strings.Join(parts, " · "))
	p.resort()
}

func (p *tasksPage) resort() {
	sign := p.table.sortSign()
	col := p.table.sortCol
	// deps reads byID, whose pointers move while the rows are sorted, so the
	// "Waits on" text is worked out first against the unsorted rows.
	p.byID = make(map[string]*fleet.TaskView, len(p.rows))
	for i := range p.rows {
		p.byID[p.rows[i].ID] = &p.rows[i]
	}
	var waits map[string]string
	if col == 4 {
		waits = make(map[string]string, len(p.rows))
		for i := range p.rows {
			waits[p.rows[i].ID] = strings.ToLower(p.deps(&p.rows[i]))
		}
	}
	slices.SortStableFunc(p.rows, func(x, y fleet.TaskView) int {
		var c int
		switch col {
		case 0:
			c = cmp.Compare(taskRank(&x), taskRank(&y))
		case 1:
			c = cmp.Compare(x.Priority, y.Priority)
		case 2:
			c = cmp.Compare(strings.ToLower(x.Title), strings.ToLower(y.Title))
		case 3:
			c = cmp.Compare(p.fleetName(x.FleetID), p.fleetName(y.FleetID))
		case 4:
			c = cmp.Compare(waits[x.ID], waits[y.ID])
		case 5:
			c = cmp.Compare(x.AgentName, y.AgentName)
		case 6:
			c = x.Created.Compare(y.Created)
		case -1: // what to do next first
			if c = cmp.Compare(taskRank(&x), taskRank(&y)); c == 0 {
				if c = cmp.Compare(y.Priority, x.Priority); c == 0 {
					c = x.Created.Compare(y.Created)
				}
			}
			return c
		default:
			return 0
		}
		return c * sign
	})
	p.byID = make(map[string]*fleet.TaskView, len(p.rows))
	keys := make([]string, len(p.rows))
	for i := range p.rows {
		p.byID[p.rows[i].ID] = &p.rows[i]
		keys[i] = p.rows[i].ID
	}
	p.list.update(keys)
	p.updateButtons()
}

func (p *tasksPage) current() *fleet.TaskView { return p.byID[p.table.selected] }

func (p *tasksPage) updateButtons() {
	t := p.current()
	queued := t != nil && t.Status == "queued"
	p.btn.edit.SetEnabled(queued)
	p.btn.dispatch.SetEnabled(queued && t.Ready)
	p.btn.cancel.SetEnabled(t != nil && (t.Status == "queued" || t.Status == "running"))
}

// fillDispatchMenu lists the agents in the task's fleet; the busy ones are
// shown greyed out so it is clear why they can't take it.
func (p *tasksPage) fillDispatchMenu() {
	p.menu.Clear()
	t := p.current()
	if t == nil || p.snap == nil {
		return
	}
	taskID := t.ID
	n := 0
	for _, ag := range p.snap.Agents {
		if ag.FleetID != t.FleetID || ag.Archived {
			continue
		}
		n++
		free := !ag.Status.Live() || ag.Status == fleet.StatusWaiting
		label := ag.Name + "  ·  " + strings.TrimPrefix(ag.Model, "claude-")
		if !free {
			label += "  ·  " + strings.ToLower(statusText(ag.Status))
		}
		act := p.menu.AddActionWithText(label)
		act.SetEnabled(free)
		id := ag.ID
		act.OnTriggered(func() { p.app.report(p.app.ctl.Dispatch(taskID, id)) })
	}
	if n == 0 {
		act := p.menu.AddActionWithText("No agents in this fleet")
		act.SetEnabled(false)
	}
}

func (p *tasksPage) cancelTask() {
	t := p.current()
	if t == nil {
		return
	}
	box := qt.NewQMessageBox6(qt.QMessageBox__Question, "Cancel task?",
		fmt.Sprintf("Take “%s” off the queue? Tasks that wait on it will stay blocked.", t.Title),
		qt.QMessageBox__Cancel, p.app.win.QWidget)
	yes := box.AddButton2("Cancel task", qt.QMessageBox__DestructiveRole)
	box.SetDefaultButtonWithButton(qt.QMessageBox__Cancel)
	box.Exec()
	if box.ClickedButton() == yes.QAbstractButton {
		p.app.report(p.app.ctl.CancelTask(t.ID))
	}
	box.DeleteLater()
}

// editTask opens the task form, empty for id == "".
func (p *tasksPage) editTask(id string) {
	if p.snap == nil {
		return
	}
	var orig *fleet.TaskView
	if id != "" {
		if orig = p.byID[id]; orig == nil || orig.Status != "queued" {
			return
		}
	}
	if len(p.snap.Fleets) == 0 {
		p.app.report(fmt.Errorf("Create a fleet first: tasks belong to a fleet."))
		return
	}
	// The dialog is modal but snapshots keep landing, so everything it reads
	// is copied now; the live lists can change or shrink under it.
	fleets := slices.Clone(p.snap.Fleets)
	rows := slices.Clone(p.rows)
	if orig != nil {
		o := *orig
		orig = &o
	}
	// Tasks that wait on this one, directly or not, can't be its own
	// dependencies: that would be a cycle.
	banned := map[string]bool{}
	if id != "" {
		banned[id] = true
		for grew := true; grew; {
			grew = false
			for _, t := range rows {
				if banned[t.ID] {
					continue
				}
				if slices.ContainsFunc(t.DependsOn, func(d string) bool { return banned[d] }) {
					banned[t.ID], grew = true, true
				}
			}
		}
	}

	dlg := qt.NewQDialog(p.app.win.QWidget)
	defer dlg.DeleteLater()
	if orig == nil {
		dlg.SetWindowTitle("New task")
	} else {
		dlg.SetWindowTitle("Edit task")
	}
	dlg.Resize(560, 520)
	form := qt.NewQFormLayout(dlg.QWidget)
	form.SetContentsMargins(18, 18, 18, 18)
	form.SetSpacing(10)

	fleetBox := qt.NewQComboBox2()
	for i, f := range fleets {
		fleetBox.AddItem(f.Name)
		if orig != nil && f.ID == orig.FleetID {
			fleetBox.SetCurrentIndex(i)
		}
	}
	fleetBox.SetEnabled(orig == nil) // dependencies are per fleet
	form.AddRow3("Fleet", fleetBox.QWidget)

	title := qt.NewQLineEdit2()
	title.SetPlaceholderText("Short name shown on the board")
	form.AddRow3("Title", title.QWidget)

	prompt := qt.NewQPlainTextEdit2()
	prompt.SetPlaceholderText("What the agent is told to do. Empty sends the title.")
	form.AddRow3("Prompt", prompt.QWidget)

	prio := qt.NewQSpinBox2()
	prio.SetRange(0, 99)
	prio.SetToolTip("Higher numbers sort first")
	form.AddRow3("Priority", prio.QWidget)

	deps := qt.NewQListWidget2()
	deps.SetMaximumHeight(150)
	form.AddRow3("Waits on", deps.QWidget)
	var depIDs []string
	fillDeps := func(fleetID string, checked []string) {
		deps.Clear()
		depIDs = depIDs[:0]
		for _, t := range rows {
			if t.FleetID != fleetID || banned[t.ID] {
				continue
			}
			it := qt.NewQListWidgetItem7(t.Title+"  ·  "+strings.ToLower(taskStatusText(&t)), deps)
			it.SetFlags(qt.ItemIsEnabled | qt.ItemIsUserCheckable)
			if slices.Contains(checked, t.ID) {
				it.SetCheckState(qt.Checked)
			} else {
				it.SetCheckState(qt.Unchecked)
			}
			depIDs = append(depIDs, t.ID)
		}
	}
	fleetAt := func() string {
		if orig != nil {
			return orig.FleetID
		}
		return fleets[min(max(0, fleetBox.CurrentIndex()), len(fleets)-1)].ID
	}
	if orig != nil {
		title.SetText(orig.Title)
		prompt.SetPlainText(orig.Prompt)
		prio.SetValue(orig.Priority)
		fillDeps(orig.FleetID, orig.DependsOn)
	} else {
		prio.SetValue(1)
		fillDeps(fleetAt(), nil)
	}
	fleetBox.OnCurrentIndexChanged(func(int) { fillDeps(fleetAt(), nil) })

	buttons := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Ok | qt.QDialogButtonBox__Cancel)
	ok := buttons.Button(qt.QDialogButtonBox__Ok)
	if orig == nil {
		ok.SetText("Add task")
	} else {
		ok.SetText("Save")
	}
	setProp(ok.QWidget, "accent", true)
	buttons.OnRejected(dlg.Reject)
	buttons.OnAccepted(func() {
		c := fleet.TaskConfig{FleetID: fleetAt(), Title: strings.TrimSpace(title.Text()),
			Prompt: strings.TrimSpace(prompt.ToPlainText()), Priority: prio.Value()}
		for i, did := range depIDs {
			if deps.Item(i).CheckState() == qt.Checked {
				c.DependsOn = append(c.DependsOn, did)
			}
		}
		if c.Title == "" {
			p.app.report(fmt.Errorf("A task needs a title."))
			return
		}
		var err error
		if orig == nil {
			_, err = p.app.ctl.AddTask(c)
		} else {
			err = p.app.ctl.UpdateTask(orig.ID, c)
		}
		if err != nil {
			p.app.report(err)
			return
		}
		dlg.Accept()
	})
	form.AddRowWithWidget(buttons.QWidget)
	title.SetFocus()
	dlg.Exec()
}
