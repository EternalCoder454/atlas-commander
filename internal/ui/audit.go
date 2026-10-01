package ui

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
	"atlas-commander/internal/store"
)

// auditPage is the append-only log, newest first, with filters and the full
// detail of the selected row. It can only read: nothing in the UI edits or
// deletes audit rows.
type auditPage struct {
	app  *App
	w    *qt.QWidget
	snap *fleet.Snapshot

	fleetBox, agentBox, kindBox *qt.QComboBox
	fleetIDs, agentIDs          []string
	search                      *qt.QLineEdit
	count                       *liveLabel

	all   []auditRow // as queried
	rows  []auditRow // after the search
	lastQ time.Time
	dirty bool
	now   time.Time
	// The query runs in a goroutine (see bgLoad); gen changes with the
	// filters so a result for old filters is dropped.
	load bgLoad[[]store.AuditEntry]
	gen  uint64

	list   *listView
	table  *dataTable
	detail *qt.QPlainTextEdit
	shown  int64
}

// auditRow is an entry with what the table and the search would otherwise
// recompute on every paint: the one-line summary and the lowercased text the
// search looks in.
type auditRow struct {
	store.AuditEntry
	summary string
	hay     string // lowercased detail and tool
}

const (
	auditLimit    = 2000
	auditInterval = 3 * time.Second
)

// auditKinds are the filter choices: a label and the stored kind.
var auditKinds = []struct{ label, kind string }{
	{"All events", ""},
	{"Tool calls", "tool_use"},
	{"Tool results", "tool_result"},
	{"Turns finished", "result"},
	{"Messages", "text"},
	{"Errors", "error"},
	{"Sessions started", "init"},
	{"Sessions ended", "exit"},
	{"Prompts sent", "user_send"},
	{"Starts", "user_start"},
	{"Stops", "user_stop"},
	{"Kills", "user_kill"},
	{"Approvals", "approval_decision"},
	{"Cost caps", "cap_reached"},
	{"Task dispatch", "task_dispatch"},
}

var auditCols = []tableCol{
	{title: "Time", width: 140, mono: true},
	{title: "Agent", width: 150},
	{title: "Event", width: 150},
	{title: "Tool", width: 100, mono: true},
	{title: "Detail", mono: true},
	{title: "Tokens", width: 80, mono: true, right: true},
	{title: "Cost", width: 90, mono: true, right: true},
}

func newAuditPage(a *App) *auditPage {
	p := &auditPage{app: a, w: qt.NewQWidget2(), dirty: true, shown: -1}
	l := newPageLayout(p.w)

	top := qt.NewQHBoxLayout2()
	top.SetSpacing(12)
	top.AddWidget(pageTitle("Audit log").QWidget)
	p.count = newLiveLabel("")
	setProp(p.count.L.QWidget, "caption", true)
	top.AddWidget(p.count.L.QWidget)
	top.AddStretch()
	export := qt.NewQPushButton3("Export CSV…")
	export.SetToolTip("Save the rows shown to a CSV file")
	export.OnClicked(p.export)
	top.AddWidget(export.QWidget)
	l.AddLayout(top.QLayout)

	filters := qt.NewQHBoxLayout2()
	filters.SetSpacing(6)
	p.fleetBox = qt.NewQComboBox2()
	p.agentBox = qt.NewQComboBox2()
	p.kindBox = qt.NewQComboBox2()
	for _, k := range auditKinds {
		p.kindBox.AddItem(k.label)
	}
	for _, b := range []*qt.QComboBox{p.fleetBox, p.agentBox, p.kindBox} {
		b.SetMinimumWidth(150)
		b.OnCurrentIndexChanged(func(int) { p.invalidate() })
		filters.AddWidget(b.QWidget)
	}
	p.search = qt.NewQLineEdit2()
	p.search.SetPlaceholderText("Search detail, tool or agent")
	p.search.SetClearButtonEnabled(true)
	p.search.OnTextChanged(func(string) { p.applySearch() })
	filters.AddWidget2(p.search.QWidget, 1)
	l.AddLayout(filters.QLayout)

	p.table = newDataTable(a, auditCols, p.cell)
	p.table.tip = func(r, c int) string {
		if r < 0 || r >= len(p.rows) {
			return ""
		}
		e := &p.rows[r]
		switch c {
		case 0:
			return e.At.Local().Format("Mon 2 Jan 2006 15:04:05.000")
		case 4:
			return e.summary
		case 6:
			if e.CostUSD > 0 {
				return fmt.Sprintf("$%.6f", e.CostUSD)
			}
		}
		return ""
	}
	p.table.onSelect = func(string) { p.showSelected() }
	p.list = newListView(p.table, "Nothing logged yet", "Every agent event and every action you take is recorded here, and never edited.")
	l.AddWidget2(p.list.W.QWidget, 3)

	p.detail = qt.NewQPlainTextEdit2()
	p.detail.SetReadOnly(true)
	setProp(p.detail.QWidget, "mono", true)
	p.detail.SetPlaceholderText("Select a row to see everything recorded for it.")
	l.AddWidget2(p.detail.QWidget, 1)
	return p
}

func (p *auditPage) widget() *qt.QWidget { return p.w }

func (p *auditPage) refresh(s *fleet.Snapshot) {
	p.snap = s
	p.now = time.Now()
	p.syncFilters(s)
	p.pickup()
	if p.dirty || time.Since(p.lastQ) >= auditInterval {
		p.query()
	}
}

// invalidate drops any result in flight and asks for a new query.
func (p *auditPage) invalidate() {
	p.gen++
	p.dirty = true
}

// syncFilters keeps the fleet and agent choices in step with what exists.
func (p *auditPage) syncFilters(s *fleet.Snapshot) {
	fids, fnames := []string{""}, []string{"All fleets"}
	for _, f := range s.Fleets {
		fids = append(fids, f.ID)
		fnames = append(fnames, f.Name)
	}
	// resetCombo blocks the box's signals, so a choice that vanished (a
	// deleted fleet or agent) must mark the query stale here.
	fleetBefore := pickID(p.fleetIDs, p.fleetBox.CurrentIndex())
	resetCombo(p.fleetBox, &p.fleetIDs, fids, fnames)
	if pickID(p.fleetIDs, p.fleetBox.CurrentIndex()) != fleetBefore {
		p.invalidate()
	}
	aids, anames := []string{""}, []string{"All agents"}
	fleetID := pickID(p.fleetIDs, p.fleetBox.CurrentIndex())
	for _, a := range s.Agents {
		if fleetID == "" || a.FleetID == fleetID {
			aids = append(aids, a.ID)
			anames = append(anames, a.Name)
		}
	}
	agentBefore := pickID(p.agentIDs, p.agentBox.CurrentIndex())
	resetCombo(p.agentBox, &p.agentIDs, aids, anames)
	if pickID(p.agentIDs, p.agentBox.CurrentIndex()) != agentBefore {
		p.invalidate()
	}
}

// resetCombo refills box when the ids changed, keeping the chosen id.
func resetCombo(box *qt.QComboBox, have *[]string, ids, names []string) {
	if slices.Equal(*have, ids) {
		return
	}
	cur := pickID(*have, box.CurrentIndex())
	*have = ids
	box.BlockSignals(true)
	box.Clear()
	box.AddItems(names)
	box.SetCurrentIndex(max(0, slices.Index(ids, cur)))
	box.BlockSignals(false)
}

func pickID(ids []string, i int) string {
	if i >= 0 && i < len(ids) {
		return ids[i]
	}
	return ""
}

// query starts the read in a goroutine; pickup applies it on a later tick.
func (p *auditPage) query() {
	f := store.Filter{
		FleetID: pickID(p.fleetIDs, p.fleetBox.CurrentIndex()),
		AgentID: pickID(p.agentIDs, p.agentBox.CurrentIndex()),
	}
	if i := p.kindBox.CurrentIndex(); i > 0 && i < len(auditKinds) {
		f.Kind = auditKinds[i].kind
	}
	ctl := p.app.ctl
	if p.load.start(p.gen, func() ([]store.AuditEntry, error) { return ctl.Audit(f, auditLimit) }) {
		p.lastQ, p.dirty = time.Now(), false
	}
}

func (p *auditPage) pickup() {
	rows, err, ok := p.load.take(p.gen)
	if !ok {
		return
	}
	if err != nil {
		p.count.Set("Couldn't read the audit log: " + err.Error())
		return
	}
	p.all = make([]auditRow, len(rows))
	for i := range rows {
		e := &rows[i]
		p.all[i] = auditRow{AuditEntry: *e, summary: auditSummary(e),
			hay: strings.ToLower(e.Detail) + "\x00" + strings.ToLower(e.Tool)}
	}
	p.applySearch()
}

func (p *auditPage) applySearch() {
	q := strings.ToLower(strings.TrimSpace(p.search.Text()))
	p.rows = p.rows[:0]
	names := map[string]string{} // lowercased agent names, once per search
	for _, e := range p.all {
		if q != "" && !strings.Contains(e.hay, q) {
			n, ok := names[e.AgentID]
			if !ok {
				n = strings.ToLower(p.agentName(e.AgentID))
				names[e.AgentID] = n
			}
			if !strings.Contains(n, q) {
				continue
			}
		}
		p.rows = append(p.rows, e)
	}
	keys := make([]string, len(p.rows))
	for i, e := range p.rows {
		keys[i] = strconv.FormatInt(e.Seq, 10)
	}
	capped := len(p.all) >= auditLimit
	switch {
	case capped && q != "":
		p.count.Set(fmt.Sprintf("%d of %d rows · newest %d only", len(p.rows), len(p.all), auditLimit))
	case capped:
		p.count.Set(fmt.Sprintf("newest %d rows · narrow the filters to see older ones", auditLimit))
	case q != "":
		p.count.Set(fmt.Sprintf("%d of %d rows", len(p.rows), len(p.all)))
	default:
		p.count.Set(plural(len(p.rows), "row"))
	}
	p.list.update(keys)
	p.showSelected()
}

func (p *auditPage) agentName(id string) string {
	if p.snap != nil {
		for _, a := range p.snap.Agents {
			if a.ID == id {
				return a.Name
			}
		}
	}
	return id
}

// kindNames are the row labels for each stored kind.
var kindNames = map[string]string{
	"tool_use": "Tool call", "tool_result": "Tool result", "result": "Turn finished", "text": "Message",
	"error": "Error", "init": "Session started", "exit": "Session ended", "usage": "Token usage",
	"user_send": "Prompt sent", "user_start": "Started", "user_stop": "Stopped", "user_kill": "Killed",
	"user_hold": "Held", "user_resume": "Resumed", "approval_decision": "Approval", "cap_reached": "Cost cap hit",
	"task_dispatch": "Task dispatched",
}

func kindLabel(kind string) string {
	if s, ok := kindNames[kind]; ok {
		return s
	}
	return kind
}

func kindTone(e *store.AuditEntry) fleet.Tone {
	switch {
	case e.IsError || e.Kind == "error":
		return fleet.ToneError
	case e.Kind == "cap_reached" || e.Kind == "approval_decision" || strings.HasPrefix(e.Kind, "user_"):
		return fleet.ToneWarn
	case e.Kind == "result" || e.Kind == "init":
		return fleet.ToneOK
	}
	return fleet.ToneIdle
}

// auditSummary is the one line a row's detail is worth: the command, path,
// text or reason, whichever it has.
func auditSummary(e *store.AuditEntry) string {
	if e.Detail == "" {
		return ""
	}
	var d map[string]any
	if json.Unmarshal([]byte(e.Detail), &d) != nil {
		return firstLine(e.Detail)
	}
	if in, ok := d["input"].(map[string]any); ok {
		for _, k := range []string{"command", "file_path", "pattern", "path", "url", "description"} {
			if s, ok := in[k].(string); ok && s != "" {
				return firstLine(s)
			}
		}
	}
	for _, k := range []string{"text", "reason", "summary", "title", "error", "command", "file_path"} {
		if s, ok := d[k].(string); ok && s != "" {
			return firstLine(s)
		}
	}
	if b, ok := d["allow"].(bool); ok {
		if b {
			return "allowed"
		}
		return "denied"
	}
	return firstLine(e.Detail)
}

func (p *auditPage) cell(r, c int) cell {
	if r < 0 || r >= len(p.rows) {
		return cell{}
	}
	e := &p.rows[r]
	switch c {
	case 0:
		at := e.At.Local()
		y, m, d := at.Date()
		ny, nm, nd := p.now.Date()
		if y == ny && m == nm && d == nd {
			return cell{text: at.Format("15:04:05"), alpha: 0.75}
		}
		return cell{text: at.Format("Jan 02 15:04:05"), alpha: 0.75}
	case 1:
		return cell{text: p.agentName(e.AgentID)}
	case 2:
		return cell{text: kindLabel(e.Kind), dot: true, dotTone: kindTone(&e.AuditEntry)}
	case 3:
		return cell{text: e.Tool, alpha: 0.75}
	case 4:
		cl := cell{text: e.summary}
		if e.IsError {
			cl.tone = fleet.ToneError
		}
		return cl
	case 5:
		if t := e.Usage.Total(); t > 0 {
			return cell{text: fmtTokens(t)}
		}
	case 6:
		if e.CostUSD > 0 {
			return cell{text: fmtUSD(e.CostUSD)}
		}
	}
	return cell{}
}

func (p *auditPage) showSelected() {
	var e *auditRow
	for i := range p.rows {
		if strconv.FormatInt(p.rows[i].Seq, 10) == p.table.selected {
			e = &p.rows[i]
		}
	}
	if e == nil {
		if p.shown != -1 {
			p.detail.Clear()
			p.shown = -1
		}
		return
	}
	if e.Seq == p.shown {
		return
	}
	p.shown = e.Seq
	var b strings.Builder
	fmt.Fprintf(&b, "#%d  %s  %s\n", e.Seq, e.At.Local().Format("2006-01-02 15:04:05.000"), kindLabel(e.Kind))
	fmt.Fprintf(&b, "agent %s  fleet %s", p.agentName(e.AgentID), e.FleetID)
	if e.SessionID != "" {
		fmt.Fprintf(&b, "  session %s", e.SessionID)
	}
	if e.TaskID != "" {
		fmt.Fprintf(&b, "  task %s", e.TaskID)
	}
	b.WriteString("\n")
	if !e.Usage.IsZero() {
		fmt.Fprintf(&b, "tokens  in %d  out %d  cache read %d  cache write %d\n",
			e.Usage.Input, e.Usage.Output, e.Usage.CacheRead, e.Usage.CacheWrite5m+e.Usage.CacheWrite1h)
	}
	if e.CostUSD > 0 {
		fmt.Fprintf(&b, "cost    $%.6f\n", e.CostUSD)
	}
	if e.Detail != "" {
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(e.Detail), "", "  ") == nil {
			b.WriteString("\n" + pretty.String())
		} else {
			b.WriteString("\n" + e.Detail)
		}
	}
	p.detail.SetPlainText(capPlain(b.String()))
}

func (p *auditPage) export() {
	if len(p.rows) == 0 {
		return
	}
	home, _ := os.UserHomeDir()
	name := "atlas-audit-" + time.Now().Format("2006-01-02") + ".csv"
	path := qt.QFileDialog_GetSaveFileName4(p.app.win.QWidget, "Export audit log", home+string(os.PathSeparator)+name, "CSV files (*.csv)")
	if path == "" {
		return
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write([]string{"seq", "time", "fleet_id", "agent_id", "agent", "session_id", "task_id", "kind", "tool",
		"input_tokens", "output_tokens", "cache_read", "cache_write", "cost_usd", "is_error", "detail"})
	for _, e := range p.rows {
		w.Write([]string{strconv.FormatInt(e.Seq, 10), e.At.UTC().Format(time.RFC3339Nano), csvSafe(e.FleetID), csvSafe(e.AgentID),
			csvSafe(p.agentName(e.AgentID)), csvSafe(e.SessionID), csvSafe(e.TaskID), csvSafe(e.Kind), csvSafe(e.Tool),
			strconv.FormatInt(e.Usage.Input, 10), strconv.FormatInt(e.Usage.Output, 10),
			strconv.FormatInt(e.Usage.CacheRead, 10), strconv.FormatInt(e.Usage.CacheWrite5m+e.Usage.CacheWrite1h, 10),
			strconv.FormatFloat(e.CostUSD, 'f', 6, 64), strconv.FormatBool(e.IsError), csvSafe(e.Detail)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		p.app.report(fmt.Errorf("Couldn't save the export: %v", err))
		return
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		p.app.report(fmt.Errorf("Couldn't save the export: %v", err))
	}
}

// csvSafe stops a spreadsheet from running a cell as a formula: agent names
// and tool output are attacker-influenced, and Excel and Sheets treat a cell
// that starts with = + - @ (or a tab or CR) as one. A leading quote makes it
// plain text.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
