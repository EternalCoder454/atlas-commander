package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/fleet"
)

// fleetsPage manages fleets and the agents registered in them: a row of
// fleet cards with their budget burn, and the selected fleet's agents below.
type fleetsPage struct {
	app  *App
	w    *qt.QWidget
	snap *fleet.Snapshot

	summary *liveLabel
	cards   *qt.QGridLayout
	cardsW  *qt.QWidget
	byFleet map[string]*fleetCard
	order   []string
	sel     string
	want    string // a fleet just created, to select once a snapshot has it
	seq     uint64 // the snapshot the cards and rows were last built from
	seqSel  string
	none    *qt.QWidget

	agentsHead *liveLabel
	rows       []fleet.AgentView
	list       *listView
	table      *dataTable

	btn struct{ editFleet, delFleet, newAgent, editAgent, archive *qt.QPushButton }
}

var fleetAgentCols = []tableCol{
	{title: "Status", width: 132},
	{title: "Agent", width: 170},
	{title: "Model", width: 150, mono: true},
	{title: "Works in"},
	{title: "Cap", width: 80, mono: true, right: true},
	{title: "Spent", width: 90, mono: true, right: true},
}

func newFleetsPage(a *App) *fleetsPage {
	p := &fleetsPage{app: a, w: qt.NewQWidget2(), byFleet: map[string]*fleetCard{}, seq: ^uint64(0)}
	l := newPageLayout(p.w)

	top := qt.NewQHBoxLayout2()
	top.SetSpacing(12)
	top.AddWidget(pageTitle("Fleets").QWidget)
	p.summary = newLiveLabel("")
	setProp(p.summary.L.QWidget, "caption", true)
	top.AddWidget(p.summary.L.QWidget)
	top.AddStretch()
	add := qt.NewQPushButton3("New fleet")
	setProp(add.QWidget, "accent", true)
	add.OnClicked(func() { p.editFleet("") })
	top.AddWidget(add.QWidget)
	l.AddLayout(top.QLayout)

	p.cardsW = qt.NewQWidget2()
	p.cards = qt.NewQGridLayout(p.cardsW)
	p.cards.SetContentsMargins(0, 0, 0, 0)
	p.cards.SetHorizontalSpacing(12)
	p.cards.SetVerticalSpacing(12)
	l.AddWidget(p.cardsW)
	p.none = emptyState("No fleets yet", "A fleet is a project folder, an optional budget and the agents that work on it. Start with New fleet.")
	l.AddWidget2(p.none, 1)

	row := qt.NewQHBoxLayout2()
	row.SetSpacing(4)
	p.agentsHead = newLiveLabel("")
	setProp(p.agentsHead.L.QWidget, "heading", true)
	row.AddWidget(p.agentsHead.L.QWidget)
	row.AddSpacing(12)
	p.btn.newAgent = qt.NewQPushButton3("New agent")
	p.btn.newAgent.OnClicked(func() { a.editAgent("", p.sel) })
	row.AddWidget(p.btn.newAgent.QWidget)
	p.btn.editAgent = qt.NewQPushButton3("Edit agent")
	p.btn.editAgent.OnClicked(func() { a.editAgent(p.table.selected, "") })
	row.AddWidget(p.btn.editAgent.QWidget)
	p.btn.archive = qt.NewQPushButton3("Archive")
	p.btn.archive.SetToolTip("Hide the agent. Its history stays in the audit log.")
	p.btn.archive.OnClicked(p.archiveAgent)
	row.AddWidget(p.btn.archive.QWidget)
	row.AddStretch()
	p.btn.editFleet = qt.NewQPushButton3("Edit fleet")
	p.btn.editFleet.OnClicked(func() { p.editFleet(p.sel) })
	row.AddWidget(p.btn.editFleet.QWidget)
	p.btn.delFleet = qt.NewQPushButton3("Delete fleet")
	setProp(p.btn.delFleet.QWidget, "danger", true)
	p.btn.delFleet.OnClicked(p.deleteFleet)
	row.AddWidget(p.btn.delFleet.QWidget)
	l.AddLayout(row.QLayout)

	p.table = newDataTable(a, fleetAgentCols, p.cell)
	p.table.onSelect = func(string) { p.updateButtons() }
	p.table.onActivate = func(id string) { a.editAgent(id, "") }
	p.table.onSort = p.resort
	p.list = newListView(p.table, "No agents in this fleet", "Register one with New agent. It gets a model, a folder, a cost cap and the tools that need your approval.")
	l.AddWidget2(p.list.W.QWidget, 1)

	a.themed = append(a.themed, func() {
		for _, c := range p.byFleet {
			c.bar.Update()
		}
	})
	p.updateButtons()
	return p
}

func (p *fleetsPage) widget() *qt.QWidget { return p.w }

func (p *fleetsPage) refresh(s *fleet.Snapshot) {
	p.snap = s
	if s.Seq == p.seq && p.sel == p.seqSel && p.want == "" {
		return // nothing new: every value shown comes from the snapshot
	}
	ids := make([]string, len(s.Fleets))
	for i, f := range s.Fleets {
		ids[i] = f.ID
	}
	if !slices.Equal(ids, p.order) {
		p.rebuildCards(s.Fleets)
	}
	if _, ok := p.byFleet[p.want]; ok {
		p.sel, p.want = p.want, ""
	}
	if _, ok := p.byFleet[p.sel]; !ok {
		p.sel = ""
		if len(ids) > 0 {
			p.sel = ids[0]
		}
	}
	agents, live, spent := 0, 0, 0.0
	for _, f := range s.Fleets {
		p.byFleet[f.ID].set(f, f.ID == p.sel)
		agents += f.Agents
		live += f.Live
		spent += f.SpentUSD
	}
	p.summary.Set(fmt.Sprintf("%s · %s · %d live · %s spent all time", plural(len(s.Fleets), "fleet"), plural(agents, "agent"), live, fmtUSD(spent)))
	p.none.SetVisible(len(ids) == 0)
	p.list.W.SetVisible(len(ids) > 0)
	p.agentsHead.L.SetVisible(len(ids) > 0)

	p.rows = p.rows[:0]
	for _, ag := range s.Agents {
		if ag.FleetID == p.sel && !ag.Archived {
			p.rows = append(p.rows, ag)
		}
	}
	if f := p.fleet(); f != nil {
		p.agentsHead.Set("Agents in " + f.Name)
	}
	p.resort()
	p.updateButtons()
	p.seq, p.seqSel = s.Seq, p.sel
}

func (p *fleetsPage) fleet() *fleet.FleetView {
	if p.snap == nil {
		return nil
	}
	for i := range p.snap.Fleets {
		if p.snap.Fleets[i].ID == p.sel {
			return &p.snap.Fleets[i]
		}
	}
	return nil
}

func (p *fleetsPage) rebuildCards(fleets []fleet.FleetView) {
	for _, c := range p.byFleet {
		p.cards.RemoveWidget(c.w.QWidget)
		c.w.Hide()
		c.w.DeleteLater()
	}
	clear(p.byFleet)
	p.order = p.order[:0]
	const perRow = 3
	for i, f := range fleets {
		c := newFleetCard(p.app, func(id string) {
			p.sel = id
			p.table.selected = ""
			if p.snap != nil {
				p.refresh(p.snap)
			}
		})
		p.byFleet[f.ID] = c
		p.order = append(p.order, f.ID)
		p.cards.AddWidget2(c.w.QWidget, i/perRow, i%perRow)
	}
	for c := range perRow {
		p.cards.SetColumnStretch(c, 1)
	}
}

func (p *fleetsPage) resort() {
	col, sign := p.table.sortCol, p.table.sortSign()
	slices.SortStableFunc(p.rows, func(x, y fleet.AgentView) int {
		var c int
		switch col {
		case 0:
			c = cmp.Compare(statusRank(x.Status), statusRank(y.Status))
		case 2:
			c = cmp.Compare(x.Model, y.Model)
		case 3:
			c = cmp.Compare(x.WorkDir, y.WorkDir)
		case 4:
			c = cmp.Compare(x.CapUSD, y.CapUSD)
		case 5:
			c = cmp.Compare(x.CostUSD, y.CostUSD)
		default:
			c = cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
		}
		return c * sign
	})
	keys := make([]string, len(p.rows))
	for i, r := range p.rows {
		keys[i] = r.ID
	}
	p.list.update(keys)
}

func (p *fleetsPage) cell(r, c int) cell {
	ag := &p.rows[r]
	switch c {
	case 0:
		return cell{text: statusText(ag.Status), dot: true, dotTone: ag.Status.Tone()}
	case 1:
		return cell{text: ag.Name}
	case 2:
		m := strings.TrimPrefix(ag.Model, "claude-")
		switch ag.Backend {
		case agent.BackendOpenAI:
			m += " · OpenAI"
		case agent.BackendGemini:
			m += " · Gemini"
		case agent.BackendLocal:
			m += " · local"
		case fleet.LegacyClaudeAPI:
			m += " · removed"
		}
		return cell{text: m, alpha: 0.8}
	case 3:
		where := shortPath(ag.WorkDir)
		if ag.UseWorktree {
			where += "  ·  own worktree"
		}
		return cell{text: where, alpha: 0.75}
	case 4:
		if ag.CapUSD > 0 {
			return cell{text: fmtCap(ag.CapUSD)}
		}
		return cell{text: "–", alpha: 0.4}
	case 5:
		return cell{text: fmtUSD(ag.CostUSD)}
	}
	return cell{}
}

func (p *fleetsPage) updateButtons() {
	has := p.fleet() != nil
	p.btn.newAgent.SetEnabled(has)
	p.btn.editFleet.SetEnabled(has)
	p.btn.delFleet.SetEnabled(has)
	var ag *fleet.AgentView
	for i := range p.rows {
		if p.rows[i].ID == p.table.selected {
			ag = &p.rows[i]
		}
	}
	p.btn.editAgent.SetEnabled(ag != nil)
	p.btn.archive.SetEnabled(ag != nil && !ag.Status.Live())
	for _, b := range []*qt.QPushButton{p.btn.newAgent, p.btn.editAgent, p.btn.archive, p.btn.editFleet, p.btn.delFleet} {
		b.SetVisible(len(p.order) > 0)
	}
}

func (p *fleetsPage) archiveAgent() {
	id := p.table.selected
	var name string
	for _, r := range p.rows {
		if r.ID == id {
			name = r.Name
		}
	}
	if name == "" {
		return
	}
	if !confirm(p.app, "Archive "+name+"?", "The agent leaves the board and can't be started again. Its sessions stay in the audit log and analytics.", "Archive") {
		return
	}
	p.app.report(p.app.ctl.ArchiveAgent(id))
}

func (p *fleetsPage) deleteFleet() {
	f := p.fleet()
	if f == nil {
		return
	}
	if f.Agents > 0 {
		p.app.report(fmt.Errorf("%s still has %s. Archive them first.", f.Name, plural(f.Agents, "agent")))
		return
	}
	if !confirm(p.app, "Delete "+f.Name+"?", "The fleet and its queued tasks are removed. The audit log keeps its history.", "Delete") {
		return
	}
	p.app.report(p.app.ctl.DeleteFleet(f.ID))
}

// confirm asks a yes/no question with a destructive yes.
func confirm(a *App, title, text, yes string) bool {
	box := qt.NewQMessageBox6(qt.QMessageBox__Warning, title, text, qt.QMessageBox__Cancel, a.win.QWidget)
	defer box.DeleteLater()
	b := box.AddButton2(yes, qt.QMessageBox__DestructiveRole)
	box.Exec()
	return box.ClickedButton() == b.QAbstractButton
}

func (p *fleetsPage) editFleet(id string) {
	a := p.app
	var orig *fleet.FleetView
	if id != "" {
		if orig = p.fleet(); orig == nil {
			return
		}
	}
	dlg := qt.NewQDialog(a.win.QWidget)
	defer dlg.DeleteLater()
	dlg.SetWindowTitle(map[bool]string{true: "New fleet", false: "Edit fleet"}[orig == nil])
	dlg.Resize(520, 0)
	form := qt.NewQFormLayout(dlg.QWidget)
	form.SetContentsMargins(18, 18, 18, 18)
	form.SetSpacing(10)

	name := qt.NewQLineEdit2()
	name.SetPlaceholderText("e.g. Billing service")
	form.AddRow3("Name", name.QWidget)

	dir, dirRow := folderPicker(a, "Project folder", "The folder agents work in unless they set their own")
	form.AddRow3("Folder", dirRow)

	budget := qt.NewQDoubleSpinBox2()
	budget.SetRange(0, 100000)
	if orig != nil {
		budget.SetMaximum(max(100000, orig.BudgetUSD))
	}
	budget.SetDecimals(2)
	budget.SetPrefix("$")
	budget.SetSpecialValueText("No budget")
	budget.SetToolTip("All-time spend across the fleet's agents. Agents are held when it is reached.")
	form.AddRow3("Budget", budget.QWidget)

	if orig != nil {
		name.SetText(orig.Name)
		dir.SetText(orig.WorkDir)
		budget.SetValue(orig.BudgetUSD)
	}

	buttons := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Ok | qt.QDialogButtonBox__Cancel)
	ok := buttons.Button(qt.QDialogButtonBox__Ok)
	ok.SetText(map[bool]string{true: "Create fleet", false: "Save"}[orig == nil])
	setProp(ok.QWidget, "accent", true)
	buttons.OnRejected(dlg.Reject)
	buttons.OnAccepted(func() {
		c := fleet.FleetConfig{Name: strings.TrimSpace(name.Text()), WorkDir: strings.TrimSpace(dir.Text()), BudgetUSD: budget.Value()}
		var err error
		if orig == nil {
			var nid string
			if nid, err = a.ctl.CreateFleet(c); err == nil {
				p.want = nid
			}
		} else {
			err = a.ctl.UpdateFleet(orig.ID, c)
		}
		if err != nil {
			a.report(err)
			return
		}
		dlg.Accept()
	})
	form.AddRowWithWidget(buttons.QWidget)
	name.SetFocus()
	dlg.Exec()
}

// folderPicker is a path field with a Browse button.
func folderPicker(a *App, caption, placeholder string) (*qt.QLineEdit, *qt.QWidget) {
	w := qt.NewQWidget2()
	l := qt.NewQHBoxLayout(w)
	l.SetContentsMargins(0, 0, 0, 0)
	l.SetSpacing(6)
	e := qt.NewQLineEdit2()
	e.SetPlaceholderText(placeholder)
	setProp(e.QWidget, "mono", true)
	l.AddWidget(e.QWidget)
	b := qt.NewQPushButton3("Browse…")
	b.OnClicked(func() {
		start := e.Text()
		if start == "" {
			start = homeDir()
		}
		if d := qt.QFileDialog_GetExistingDirectory3(a.win.QWidget, caption, start); d != "" {
			e.SetText(d)
		}
	})
	l.AddWidget(b.QWidget)
	return e, w
}

// fleetCard shows one fleet: its name and folder, agent counts, and its
// spend against the budget as a bar.
type fleetCard struct {
	app      *App
	w        *qt.QFrame
	name     *liveLabel
	dir      *liveLabel
	counts   *liveLabel
	money    *liveLabel
	bar      *qt.QWidget
	frac     float64
	selected bool
	id       string
}

func newFleetCard(a *App, onPick func(id string)) *fleetCard {
	c := &fleetCard{app: a, w: qt.NewQFrame2(), name: newLiveLabel(""), dir: newLiveLabel(""), counts: newLiveLabel(""), money: newLiveLabel("")}
	setProp(c.w.QWidget, "card", true)
	pointer(c.w.QWidget)
	c.w.SetMinimumHeight(118)
	l := qt.NewQVBoxLayout(c.w.QWidget)
	l.SetContentsMargins(16, 12, 16, 14)
	l.SetSpacing(3)
	head := qt.NewQHBoxLayout2()
	setProp(c.name.L.QWidget, "heading", true)
	head.AddWidget(c.name.L.QWidget)
	head.AddStretch()
	setProp(c.counts.L.QWidget, "caption", true)
	head.AddWidget(c.counts.L.QWidget)
	l.AddLayout(head.QLayout)
	setProp(c.dir.L.QWidget, "caption", true)
	setProp(c.dir.L.QWidget, "mono", true)
	l.AddWidget(c.dir.L.QWidget)
	l.AddStretch()
	setProp(c.money.L.QWidget, "mono", true)
	l.AddWidget(c.money.L.QWidget)
	c.bar = qt.NewQWidget2()
	c.bar.SetFixedHeight(6)
	c.bar.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) { c.paintBar() })
	l.AddWidget(c.bar)
	c.w.OnMousePressEvent(func(super func(*qt.QMouseEvent), ev *qt.QMouseEvent) { onPick(c.id) })
	c.w.OnPaintEvent(func(super func(*qt.QPaintEvent), ev *qt.QPaintEvent) {
		super(ev)
		if c.selected {
			p := qt.NewQPainter2(c.w.QPaintDevice)
			p.SetRenderHint(qt.QPainter__Antialiasing)
			strokeRound(p, 1, 1, float64(c.w.Width())-2, float64(c.w.Height())-2, 8, a.pal.accentB, 1, 2)
			p.End()
			p.Delete()
		}
	})
	return c
}

func (c *fleetCard) set(f fleet.FleetView, selected bool) {
	c.id = f.ID
	c.name.Set(f.Name)
	if c.dir.last != shortPath(f.WorkDir) {
		c.dir.L.SetToolTip(f.WorkDir)
	}
	c.dir.Set(shortPath(f.WorkDir))
	counts := plural(f.Agents, "agent")
	if f.Live > 0 {
		counts += fmt.Sprintf(" · %d live", f.Live)
	}
	c.counts.Set(counts)
	if f.BudgetUSD > 0 {
		c.money.Set(fmt.Sprintf("%s of %s", fmtUSD(f.SpentUSD), fmtCap(f.BudgetUSD)))
	} else {
		c.money.Set(fmtUSD(f.SpentUSD) + " · no budget")
	}
	frac := -1.0
	if f.BudgetUSD > 0 {
		frac = f.SpentUSD / f.BudgetUSD
	}
	if frac != c.frac {
		c.frac = frac
		c.bar.Update()
	}
	if selected != c.selected {
		c.selected = selected
		c.w.Update()
	}
}

func (c *fleetCard) paintBar() {
	pal := c.app.pal
	p := qt.NewQPainter2(c.bar.QPaintDevice)
	defer p.Delete()
	defer p.End()
	p.SetRenderHint(qt.QPainter__Antialiasing)
	w, h := float64(c.bar.Width()), float64(c.bar.Height())
	fillRound(p, 0, 0, w, h, h/2, pal.fg, 0.08)
	if c.frac < 0 {
		return
	}
	f := min(c.frac, 1)
	col := pal.accentB
	switch {
	case c.frac >= 1:
		col = pal.err
	case c.frac >= 0.8:
		col = pal.warn
	}
	if f > 0 {
		fillRound(p, 0, 0, max(h, w*f), h, h/2, col, 1)
	}
}

// approvalTools are the tools offered in the registration form's approval
// list, in the order Claude Code documents them.
var approvalTools = []string{"Bash", "Write", "Edit", "MultiEdit", "NotebookEdit", "WebFetch", "WebSearch", "Task"}

// modelChoices are offered in the model box; any other id can be typed.
var modelChoices = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5", "claude-fable-5-1"}

// providerChoices are the providers in the editor, in the order they are
// shown. Index 0 is Claude Code, the only one that runs tools.
var providerChoices = []struct{ backend, label string }{
	{agent.BackendClaudeCode, "Claude Code"},
	{agent.BackendOpenAI, "OpenAI"},
	{agent.BackendGemini, "Gemini"},
	{agent.BackendLocal, "Local"},
}

// providerIndex is the position of a backend in providerChoices. A backend the
// editor does not offer, such as the removed Claude API, gives Claude Code.
func providerIndex(backend string) int {
	for i, p := range providerChoices {
		if p.backend == backend {
			return i
		}
	}
	return 0
}

// editAgent is the registration form, for a new agent (id "") in fleetID, or
// to change an existing one.
func (a *App) editAgent(id, fleetID string) {
	if a.snap == nil || len(a.snap.Fleets) == 0 {
		a.report(fmt.Errorf("Create a fleet first: every agent belongs to one."))
		return
	}
	var orig fleet.AgentConfig
	if id != "" {
		var err error
		if orig, err = a.ctl.AgentConfig(id); err != nil {
			a.report(err)
			return
		}
		fleetID = orig.FleetID
	} else {
		orig = fleet.AgentConfig{FleetID: fleetID, Backend: agent.BackendClaudeCode, Model: modelChoices[1], Approve: fleet.DefaultApprove}
	}
	if fleetID == "" {
		fleetID = a.settings.DefaultFleet
	}

	dlg := qt.NewQDialog(a.win.QWidget)
	defer dlg.DeleteLater()
	dlg.SetWindowTitle(map[bool]string{true: "New agent", false: "Edit agent"}[id == ""])
	dlg.Resize(600, 0)
	form := qt.NewQFormLayout(dlg.QWidget)
	form.SetContentsMargins(18, 18, 18, 18)
	form.SetSpacing(10)

	name := qt.NewQLineEdit2()
	name.SetPlaceholderText("e.g. reviewer, migrations, docs")
	name.SetText(orig.Name)
	form.AddRow3("Name", name.QWidget)

	// The fleets as they were when the dialog opened: the snapshot moves on
	// under it while it is open.
	fleets := slices.Clone(a.snap.Fleets)
	fleetBox := qt.NewQComboBox2()
	for i, f := range fleets {
		fleetBox.AddItem(f.Name)
		if f.ID == fleetID {
			fleetBox.SetCurrentIndex(i)
		}
	}
	fleetBox.SetEnabled(id == "")
	form.AddRow3("Fleet", fleetBox.QWidget)
	fleetAt := func() fleet.FleetView {
		if i := fleetBox.CurrentIndex(); i >= 0 && i < len(fleets) {
			return fleets[i]
		}
		return fleet.FleetView{ID: fleetID}
	}

	labels := make([]string, len(providerChoices))
	for i, p := range providerChoices {
		labels[i] = p.label
	}
	backend := segmentedValue(labels, providerIndex(orig.Backend))
	form.AddRow3("Runs on", backend.w)
	backendNote := caption("")
	backendNote.SetWordWrap(true)
	form.AddRowWithWidget(backendNote.QWidget)

	model := qt.NewQComboBox2()
	model.SetEditable(true)
	model.AddItems(modelChoices)
	model.SetCurrentText(orig.Model)
	setProp(model.QWidget, "mono", true)
	model.LineEdit().SetPlaceholderText("Pick a model or type its id")
	form.AddRow3("Model", model.QWidget)
	modelNote := caption("")
	modelNote.SetWordWrap(true)
	form.AddRowWithWidget(modelNote.QWidget)

	dir, dirRow := folderPicker(a, "Agent folder", "")
	dir.SetText(orig.WorkDir)
	form.AddRow3("Folder", dirRow)
	setDirHint := func() { dir.SetPlaceholderText("The fleet's: " + shortPath(fleetAt().WorkDir)) }
	setDirHint()
	fleetBox.OnCurrentIndexChanged(func(int) { setDirHint() })

	worktree := qt.NewQCheckBox3("Give it its own git worktree and branch")
	worktree.SetToolTip("Lets several agents edit the same repository without stepping on each other")
	worktree.SetChecked(orig.UseWorktree)
	form.AddRow3("", worktree.QWidget)

	pinned := qt.NewQPlainTextEdit2()
	pinned.SetPlaceholderText("Sent at the start of every session: its role, conventions, what not to touch.")
	pinned.SetPlainText(orig.PinnedPrompt)
	pinned.SetFixedHeight(96)
	form.AddRow3("Pinned prompt", pinned.QWidget)

	capBox := qt.NewQDoubleSpinBox2()
	capBox.SetRange(0, max(10000, orig.CostCapUSD))
	capBox.SetDecimals(2)
	capBox.SetPrefix("$")
	capBox.SetSpecialValueText("No cap")
	capBox.SetValue(orig.CostCapUSD)
	capBox.SetToolTip("All-time spend. The agent is held when it reaches the cap.")
	form.AddRow3("Cost cap", capBox.QWidget)

	every := qt.NewQCheckBox3("Every tool asks first")
	every.SetChecked(slices.Contains(orig.Approve, "*"))
	tools := qt.NewQWidget2()
	tg := qt.NewQGridLayout(tools)
	tg.SetContentsMargins(0, 0, 0, 0)
	tg.SetHorizontalSpacing(14)
	var checks []*qt.QCheckBox
	for i, t := range approvalTools {
		c := qt.NewQCheckBox3(t)
		c.SetChecked(slices.Contains(orig.Approve, t))
		setProp(c.QWidget, "mono", true)
		tg.AddWidget2(c.QWidget, i/4, i%4)
		checks = append(checks, c)
	}
	sync := func() {
		for _, c := range checks {
			c.SetEnabled(!every.IsChecked())
		}
	}
	every.OnToggled(func(bool) { sync() })
	sync()
	approve := qt.NewQWidget2()
	al := qt.NewQVBoxLayout(approve)
	al.SetContentsMargins(0, 0, 0, 0)
	al.SetSpacing(6)
	al.AddWidget(tools)
	al.AddWidget(every.QWidget)
	note := caption("Checked tools wait on the Approvals page before they run. Everything else runs freely.")
	note.SetWordWrap(true)
	al.AddWidget(note.QWidget)
	form.AddRow3("Needs approval", approve)

	// The model list of a chat provider comes from the network, so it loads
	// in the background and a timer on this thread picks the result up; the
	// goroutine never touches a widget. want counts provider switches, so a
	// list that arrives after the user moved on is dropped.
	var models bgLoad[[]string]
	var want uint64
	started := true
	loadModels := func(i int) {
		want++
		model.Clear()
		if i == 0 {
			model.AddItems(modelChoices)
			modelNote.SetText("")
			started = true
			return
		}
		modelNote.SetText("Loading models…")
		started = false
	}
	timer := qt.NewQTimer2(dlg.QObject)
	timer.OnTimeout(func() {
		cur := backend.sel
		if cur == 0 {
			return
		}
		if list, err, ok := models.take(want); ok {
			switch {
			case err != nil:
				modelNote.SetText(err.Error() + " You can still type a model id.")
			case len(list) == 0:
				modelNote.SetText("No models found. You can type a model id.")
			default:
				typed := model.CurrentText()
				model.AddItems(list)
				model.SetCurrentText(typed)
				if strings.TrimSpace(typed) == "" {
					model.SetCurrentText(list[0])
				}
				modelNote.SetText("")
			}
		}
		if !started {
			name, gen := providerChoices[cur].backend, want
			started = models.start(gen, func() ([]string, error) { return a.ctl.Models(name) })
		}
	})
	timer.Start(150)

	setBackend := func(i int) {
		chat := i != 0
		worktree.SetEnabled(!chat)
		approve.SetEnabled(!chat)
		worktree.SetVisible(!chat)
		approve.SetVisible(!chat)
		form.LabelForField(approve).SetVisible(!chat)
		switch providerChoices[i].backend {
		case agent.BackendOpenAI:
			backendNote.SetText("Chats with OpenAI using the key in $" + a.settings.OpenAIKeyEnv + ". It answers in text and does not run tools.")
		case agent.BackendGemini:
			backendNote.SetText("Chats with Google Gemini using the key in $" + a.settings.GeminiKeyEnv + ". It answers in text and does not run tools.")
		case agent.BackendLocal:
			backendNote.SetText("Runs on this machine through Ollama and costs nothing. It answers in text and does not run tools.")
		default:
			backendNote.SetText("Runs the claude CLI in the folder below, with your Claude Code login, settings and tools.")
		}
	}
	backend.on = func(i int) {
		setBackend(i)
		loadModels(i)
		// Keep the saved model when coming back to the saved provider;
		// otherwise start from a sensible one (Claude) or the loaded list.
		switch {
		case providerChoices[i].backend == orig.Backend:
			model.SetCurrentText(orig.Model)
		case i == 0:
			model.SetCurrentText(modelChoices[1])
		}
	}
	setBackend(backend.sel)
	if backend.sel != 0 {
		loadModels(backend.sel)
		model.SetCurrentText(orig.Model)
	}

	buttons := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Ok | qt.QDialogButtonBox__Cancel)
	ok := buttons.Button(qt.QDialogButtonBox__Ok)
	ok.SetText(map[bool]string{true: "Register agent", false: "Save"}[id == ""])
	setProp(ok.QWidget, "accent", true)
	buttons.OnRejected(dlg.Reject)
	buttons.OnAccepted(func() {
		fid := fleetAt().ID
		if id != "" {
			fid = orig.FleetID // the fleet can't change once registered
		}
		c := fleet.AgentConfig{
			FleetID:      fid,
			Name:         strings.TrimSpace(name.Text()),
			Backend:      providerChoices[backend.sel].backend,
			Model:        strings.TrimSpace(model.CurrentText()),
			WorkDir:      strings.TrimSpace(dir.Text()),
			PinnedPrompt: strings.TrimSpace(pinned.ToPlainText()),
			CostCapUSD:   capBox.Value(),
			UseWorktree:  worktree.IsChecked(),
			Approve:      []string{},
		}
		if c.Backend != agent.BackendClaudeCode {
			// Chat providers run no tools and have no folder of their own
			// to branch: the hidden choices are not sent.
			c.UseWorktree = false
			c.Approve = slices.Clone(orig.Approve)
			if c.Approve == nil {
				c.Approve = []string{}
			}
		} else if every.IsChecked() {
			c.Approve = []string{"*"}
		} else {
			for i, ch := range checks {
				if ch.IsChecked() {
					c.Approve = append(c.Approve, approvalTools[i])
				}
			}
			// Keep tools set elsewhere that the form doesn't list.
			for _, t := range orig.Approve {
				if t != "*" && !slices.Contains(approvalTools, t) {
					c.Approve = append(c.Approve, t)
				}
			}
		}
		var err error
		if id == "" {
			_, err = a.ctl.RegisterAgent(c)
		} else {
			err = a.ctl.UpdateAgent(id, c)
		}
		if err != nil {
			a.report(err)
			return
		}
		dlg.Accept()
	})
	form.AddRowWithWidget(buttons.QWidget)
	name.SetFocus()
	dlg.Exec()
}

// segValue is a segmented control that remembers its choice.
type segValue struct {
	w   *qt.QWidget
	sel int
	on  func(int)
}

func segmentedValue(labels []string, sel int) *segValue {
	s := &segValue{sel: sel}
	s.w = segmented(labels, sel, func(i int) {
		s.sel = i
		if s.on != nil {
			s.on(i)
		}
	})
	return s
}
