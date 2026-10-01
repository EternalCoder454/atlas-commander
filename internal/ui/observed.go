package ui

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/agent"
	"atlas-commander/internal/fleet"
	"atlas-commander/internal/observed"
)

// observedPage lists Claude Code sessions started outside Commander, from
// their transcripts on disk, and shows one read-only. Commander cannot steer
// these; it only watches.
type observedPage struct {
	app *App
	w   *qt.QWidget
	now time.Time

	rows    []observed.Session
	lastQ   time.Time
	summary *liveLabel
	list    *listView
	table   *dataTable

	head    *qt.QLabel
	log     *qt.QPlainTextEdit
	shown   string // path in the transcript view
	shownAt time.Time
	lastLog time.Time

	// Both reads run in a goroutine (see bgLoad) so a big transcript or a
	// slow disk never stalls the Qt thread.
	sessLoad bgLoad[[]observed.Session]
	txLoad   bgLoad[txResult]
	txGen    uint64 // changes with the selection; stale results are dropped
	loaded   txKey  // what the view shows now: the cache key
	prev     []observed.Line
	headText string
}

// txKey identifies one state of a transcript file. A read whose key matches
// the one on screen is skipped.
type txKey struct {
	path string
	size int64
	mod  time.Time
}

// txResult is what the transcript goroutine hands back.
type txResult struct {
	key   txKey
	same  bool // the file is unchanged since the key asked about
	lines []observed.Line
}

// observedMaxLines is how many of a transcript's last lines the view keeps.
const observedMaxLines = 2000

const (
	observedInterval = 5 * time.Second
	observedLive     = 2 * time.Second // transcript re-read while its session is live
)

var observedCols = []tableCol{
	{title: "Status", width: 96},
	{title: "Session"},
	{title: "Project", width: 200},
	{title: "Model", width: 110, mono: true},
	{title: "Recent msgs", width: 100, mono: true, right: true},
	{title: "Updated", width: 86, mono: true, right: true},
}

func newObservedPage(a *App) *observedPage {
	p := &observedPage{app: a, w: qt.NewQWidget2()}
	l := newPageLayout(p.w)

	top := qt.NewQHBoxLayout2()
	top.SetSpacing(12)
	top.AddWidget(pageTitle("Observed").QWidget)
	p.summary = newLiveLabel("")
	setProp(p.summary.L.QWidget, "caption", true)
	top.AddWidget(p.summary.L.QWidget)
	top.AddStretch()
	l.AddLayout(top.QLayout)

	p.table = newDataTable(a, observedCols, p.cell)
	p.table.tip = func(r, c int) string {
		if r < 0 || r >= len(p.rows) {
			return ""
		}
		s := &p.rows[r]
		switch c {
		case 1:
			return s.Title
		case 2:
			if s.GitBranch != "" {
				return s.Project + " on " + s.GitBranch
			}
			return s.Project
		}
		return ""
	}
	p.table.onSelect = func(string) { p.showSelected(true) }
	p.table.onSort = p.resort
	p.list = newListView(p.table, "No Claude Code sessions found",
		"Sessions you run in a terminal show up here, read-only, from Claude Code's transcripts in "+observed.Root()+".")
	l.AddWidget2(p.list.W.QWidget, 2)

	p.head = caption("")
	p.head.SetWordWrap(true)
	l.AddWidget(p.head.QWidget)
	p.log = qt.NewQPlainTextEdit2()
	p.log.SetReadOnly(true)
	p.log.SetMaximumBlockCount(5000)
	p.log.SetPlaceholderText("Select a session to read its transcript.")
	l.AddWidget2(p.log.QWidget, 3)
	return p
}

func (p *observedPage) widget() *qt.QWidget { return p.w }

func (p *observedPage) refresh(_ *fleet.Snapshot) {
	p.now = time.Now()
	p.pickupSessions()
	if time.Since(p.lastQ) >= observedInterval {
		p.query()
	} else {
		p.table.T.Viewport().Update()
	}
	p.pickupTranscript()
	if s := p.current(); s != nil && time.Since(p.lastLog) >= observedLive && (p.shown != s.Path || s.Live || s.Modified.After(p.shownAt)) {
		p.showSelected(false)
	}
}

// query starts the session scan in a goroutine; pickupSessions applies it.
func (p *observedPage) query() {
	ctl := p.app.ctl
	if p.sessLoad.start(0, func() ([]observed.Session, error) { return ctl.ObservedSessions() }) {
		p.lastQ = time.Now()
	}
}

func (p *observedPage) pickupSessions() {
	rows, err, ok := p.sessLoad.take(0)
	if !ok {
		return
	}
	if err != nil {
		p.summary.Set("Couldn't read Claude Code's sessions: " + err.Error())
		return
	}
	p.rows = slices.Clone(rows) // sorted in place below; the controller owns rows
	live := 0
	for _, s := range rows {
		if s.Live {
			live++
		}
	}
	sum := plural(len(rows), "session")
	if live > 0 {
		sum += fmt.Sprintf(" · %d live", live)
	}
	p.summary.Set(sum + " · read-only")
	p.resort()
}

func (p *observedPage) resort() {
	col, sign := p.table.sortCol, p.table.sortSign()
	slices.SortStableFunc(p.rows, func(x, y observed.Session) int {
		var c int
		switch col {
		case 0:
			c = cmp.Compare(boolRank(y.Live), boolRank(x.Live))
		case 1:
			c = cmp.Compare(strings.ToLower(x.Title), strings.ToLower(y.Title))
		case 2:
			c = cmp.Compare(x.Project, y.Project)
		case 3:
			c = cmp.Compare(x.Model, y.Model)
		case 4:
			c = cmp.Compare(x.Messages, y.Messages)
		case 5:
			c = x.Modified.Compare(y.Modified)
		case -1: // newest first
			return y.Modified.Compare(x.Modified)
		default:
			return 0
		}
		return c * sign
	})
	keys := make([]string, len(p.rows))
	for i, s := range p.rows {
		keys[i] = s.Path
	}
	p.list.update(keys)
	if p.table.selected == "" && len(keys) > 0 {
		p.table.selectKey(keys[0])
	}
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (p *observedPage) cell(r, c int) cell {
	if r < 0 || r >= len(p.rows) {
		return cell{}
	}
	s := &p.rows[r]
	switch c {
	case 0:
		if s.Live {
			return cell{text: "Live", dot: true, dotTone: fleet.ToneOK}
		}
		return cell{text: "Ended", dot: true, dotTone: fleet.ToneIdle, alpha: 0.75}
	case 1:
		if s.Title == "" {
			return cell{text: "(no prompt yet)", alpha: 0.5}
		}
		return cell{text: s.Title}
	case 2:
		return cell{text: shortPath(s.Project), alpha: 0.75}
	case 3:
		return cell{text: strings.TrimPrefix(s.Model, "claude-"), alpha: 0.75}
	case 4:
		if s.Messages > 0 {
			return cell{text: fmt.Sprint(s.Messages)}
		}
	case 5:
		return cell{text: fmtAgo(s.Modified, p.now), alpha: 0.75}
	}
	return cell{}
}

// shortPath shows a project as ~/… when it is under the home directory.
func shortPath(path string) string {
	if home := homeDir(); home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

func (p *observedPage) current() *observed.Session {
	for i := range p.rows {
		if p.rows[i].Path == p.table.selected {
			return &p.rows[i]
		}
	}
	return nil
}

// showSelected asks for the selected transcript. A live one is re-read on a
// clock; the work happens in a goroutine and pickupTranscript draws it.
func (p *observedPage) showSelected(changed bool) {
	s := p.current()
	if s == nil {
		p.txGen++
		p.shown, p.prev, p.loaded, p.headText = "", nil, txKey{}, ""
		p.head.SetText("")
		p.log.Clear()
		return
	}
	if changed && s.Path != p.shown {
		// A different transcript: drop the old one now and any read of it.
		p.txGen++
		p.shown, p.prev, p.loaded = "", nil, txKey{}
		p.log.Clear()
	}
	// Cached by the session's size and mtime from the last listing, so an
	// ended transcript is not parsed again. A live one is re-read on its
	// clock: the listing is refreshed less often than it grows. The key comes
	// from the controller, not os.Stat, since the path is the controller's to
	// interpret.
	path, last, live := s.Path, p.loaded, s.Live
	key := txKey{path, s.Size, s.Modified}
	if p.txLoad.start(p.txGen, func() (txResult, error) {
		if key == last && !live {
			return txResult{key: key, same: true}, nil
		}
		lines, err := p.app.ctl.ObservedTranscript(path)
		return txResult{key: key, lines: lines}, err
	}) {
		p.lastLog = time.Now()
	}
}

func (p *observedPage) pickupTranscript() {
	res, err, ok := p.txLoad.take(p.txGen)
	if !ok {
		return
	}
	if err != nil {
		p.head.SetText("Couldn't read this transcript: " + err.Error())
		p.headText = ""
		p.log.Clear()
		p.shown, p.prev, p.loaded = "", nil, txKey{}
		return
	}
	s := p.current()
	if s == nil || s.Path != res.key.path {
		return
	}
	if res.same {
		p.shownAt = s.Modified
		return
	}
	sb := p.log.VerticalScrollBar()
	pinned := s.Path != p.shown || sb.Value() >= sb.Maximum()-4
	p.shown, p.shownAt, p.loaded = s.Path, s.Modified, res.key

	head := shortPath(s.Project)
	if s.GitBranch != "" {
		head += " · " + s.GitBranch
	}
	if s.Model != "" {
		head += " · " + s.Model
	}
	head += " · started " + s.Started.Local().Format("Jan 2 15:04")
	if head != p.headText {
		p.headText = head
		p.head.SetText(head)
	}

	// When the file only grew, the lines already shown are a prefix of the
	// new ones and just the rest is appended, which keeps the reader's
	// selection and scroll position. Anything else (the window slid, the file
	// was rewritten) reloads the view.
	lines := res.lines
	from := 0
	if n := len(p.prev); n > 0 && n <= len(lines) && slices.Equal(p.prev, lines[:n]) {
		from = n
	} else {
		p.log.Clear()
	}
	if from < len(lines) {
		p.log.SetUpdatesEnabled(false)
		for _, ln := range lines[from:] {
			p.log.AppendHtml(entryHTML(p.app, observedEntry(ln)))
		}
		p.log.SetUpdatesEnabled(true)
	}
	p.prev = lines
	if pinned {
		sb.SetValue(sb.Maximum())
	}
}

// observedEntry maps a transcript line onto the detail view's entry, so both
// read the same.
func observedEntry(l observed.Line) fleet.Entry {
	e := fleet.Entry{At: l.At, Text: l.Text, Tool: l.Tool, IsError: l.IsError}
	switch l.Role {
	case "user":
		e.User = true
	case "tool":
		e.Kind = agent.EventToolUse
	case "result":
		e.Kind = agent.EventToolResult
	case "system":
		e.Kind = agent.EventInit
	default:
		e.Kind = agent.EventText
	}
	return e
}
