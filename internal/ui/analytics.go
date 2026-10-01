package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
	"atlas-commander/internal/store"
)

// analyticsPage answers "where did the money go": totals over a time range,
// spend and output over time, and breakdowns per agent and per session. It
// queries the audit log, so it refreshes on a slow clock, not every tick.
type analyticsPage struct {
	app  *App
	w    *qt.QWidget
	snap *fleet.Snapshot

	rangeIdx int
	fleetID  string
	fleets   *qt.QComboBox
	fleetIDs []string
	lastQ    time.Time
	err      *qt.QLabel
	// The queries run in a goroutine (see bgLoad). gen changes whenever the
	// filter does, so a result for the old filter is dropped; dirty asks for
	// a fresh query as soon as none is running.
	load  bgLoad[analyticsData]
	gen   uint64
	dirty bool

	tiles struct{ spend, turns, success, tools, tokens, eff *statTile }
	spend *Chart
	out   *Chart

	byAgent   bool
	agents    []store.AgentStats
	sessions  []store.SessionStats
	agentT    *dataTable
	sessionT  *dataTable
	tables    *qt.QStackedWidget
	agentList *listView
	sessList  *listView
}

// analyticsData is everything one refresh reads from the audit log.
type analyticsData struct {
	f        store.Filter
	r        timeRange
	tot      store.Totals
	series   []store.CostPoint
	agents   []store.AgentStats
	sessions []store.SessionStats
}

// sessionRowLimit caps the per-session table: the audit log can hold
// thousands of sessions and the table is only ever read from the top.
const sessionRowLimit = 500

type timeRange struct {
	label  string
	span   time.Duration // 0 = all time
	bucket time.Duration
	unit   string
}

var timeRanges = []timeRange{
	{"24 hours", 24 * time.Hour, time.Hour, "hour"},
	{"7 days", 7 * 24 * time.Hour, 6 * time.Hour, "6 hours"},
	{"30 days", 30 * 24 * time.Hour, 24 * time.Hour, "day"},
	{"All time", 0, 24 * time.Hour, "day"},
}

const analyticsInterval = 5 * time.Second

var agentStatCols = []tableCol{
	{title: "Agent"},
	{title: "Turns", width: 70, mono: true, right: true},
	{title: "Success", width: 80, mono: true, right: true},
	{title: "Tool calls", width: 90, mono: true, right: true},
	{title: "Errors", width: 70, mono: true, right: true},
	{title: "Tokens", width: 90, mono: true, right: true},
	{title: "Cost", width: 96, mono: true, right: true},
	{title: "Output per $", width: 110, mono: true, right: true},
}

var sessionStatCols = []tableCol{
	{title: "Session", width: 130, mono: true},
	{title: "Agent"},
	{title: "Started", width: 130, mono: true},
	{title: "Length", width: 80, mono: true, right: true},
	{title: "Turns", width: 70, mono: true, right: true},
	{title: "Success", width: 80, mono: true, right: true},
	{title: "Tokens", width: 90, mono: true, right: true},
	{title: "Cost", width: 96, mono: true, right: true},
}

func newAnalyticsPage(a *App) *analyticsPage {
	p := &analyticsPage{app: a, w: qt.NewQWidget2(), rangeIdx: 1, byAgent: true, dirty: true}
	l := newPageLayout(p.w)

	top := qt.NewQHBoxLayout2()
	top.SetSpacing(12)
	top.AddWidget(pageTitle("Analytics").QWidget)
	top.AddStretch()
	p.fleets = qt.NewQComboBox2()
	p.fleets.SetMinimumWidth(160)
	p.fleets.OnCurrentIndexChanged(func(i int) {
		if i >= 0 && i < len(p.fleetIDs) && p.fleetIDs[i] != p.fleetID {
			p.fleetID = p.fleetIDs[i]
			p.invalidate()
		}
	})
	top.AddWidget(p.fleets.QWidget)
	labels := make([]string, len(timeRanges))
	for i, r := range timeRanges {
		labels[i] = r.label
	}
	top.AddWidget(dropdown(labels, p.rangeIdx, func(i int) { p.rangeIdx = i; p.invalidate() }).QWidget)
	l.AddLayout(top.QLayout)

	p.err = caption("")
	setProp(p.err.QWidget, "status", "error")
	p.err.Hide()
	l.AddWidget(p.err.QWidget)

	tiles := qt.NewQHBoxLayout2()
	tiles.SetSpacing(10)
	for _, t := range []struct {
		dst   **statTile
		title string
	}{
		{&p.tiles.spend, "Spend"}, {&p.tiles.turns, "Turns"}, {&p.tiles.success, "Success rate"},
		{&p.tiles.tools, "Tool calls"}, {&p.tiles.tokens, "Tokens"}, {&p.tiles.eff, "Output per dollar"},
	} {
		*t.dst = newStatTile(t.title)
		tiles.AddWidget((*t.dst).W.QWidget)
	}
	l.AddLayout(tiles.QLayout)

	charts := qt.NewQHBoxLayout2()
	charts.SetSpacing(18)
	p.spend = newChart(a, "Spend", "", 150, fmtUSD)
	p.out = newChart(a, "Output tokens", "", 150, func(v float64) string { return fmtTokens(int64(v)) })
	charts.AddWidget(p.spend.W)
	charts.AddWidget(p.out.W)
	l.AddLayout(charts.QLayout)

	tabs := qt.NewQHBoxLayout2()
	tabs.AddWidget(dropdown([]string{"By agent", "By session"}, 0, func(i int) {
		p.byAgent = i == 0
		if p.byAgent {
			p.tables.SetCurrentWidget(p.agentList.W.QWidget)
		} else {
			p.tables.SetCurrentWidget(p.sessList.W.QWidget)
		}
	}).QWidget)
	tabs.AddStretch()
	l.AddLayout(tabs.QLayout)

	p.agentT = newDataTable(a, agentStatCols, p.agentCell)
	p.agentT.onSort = p.sortAgents
	p.agentT.onActivate = func(id string) { a.openAgent(id) }
	p.sessionT = newDataTable(a, sessionStatCols, p.sessionCell)
	p.sessionT.onSort = p.sortSessions
	p.sessionT.tip = func(r, c int) string {
		if c == 0 && r >= 0 && r < len(p.sessions) {
			return p.sessions[r].SessionID
		}
		return ""
	}
	p.agentList = newListView(p.agentT, "No activity in this range", "This page shows what your agents spent. Numbers appear once an agent has finished a turn, or try a wider date range.")
	p.sessList = newListView(p.sessionT, "No sessions in this range", "Each time you start an agent it is a session. They are listed here once they have run.")
	p.tables = qt.NewQStackedWidget2()
	p.tables.AddWidget(p.agentList.W.QWidget)
	p.tables.AddWidget(p.sessList.W.QWidget)
	l.AddWidget2(p.tables.QWidget, 1)
	return p
}

func (p *analyticsPage) widget() *qt.QWidget { return p.w }

func (p *analyticsPage) refresh(s *fleet.Snapshot) {
	p.snap = s
	p.syncFleets(s)
	p.pickup()
	if p.dirty || time.Since(p.lastQ) >= analyticsInterval {
		p.query()
	}
}

// invalidate drops any result in flight and asks for a new query.
func (p *analyticsPage) invalidate() {
	p.gen++
	p.dirty = true
}

// syncFleets keeps the fleet filter in step with the fleets that exist.
func (p *analyticsPage) syncFleets(s *fleet.Snapshot) {
	ids := []string{""}
	names := []string{"All fleets"}
	for _, f := range s.Fleets {
		ids = append(ids, f.ID)
		names = append(names, f.Name)
	}
	if slices.Equal(ids, p.fleetIDs) {
		return
	}
	cur := p.fleetID
	p.fleetIDs = ids
	p.fleets.BlockSignals(true)
	p.fleets.Clear()
	p.fleets.AddItems(names)
	i := max(0, slices.Index(ids, cur))
	p.fleets.SetCurrentIndex(i)
	p.fleets.BlockSignals(false)
	if p.fleetID = ids[i]; p.fleetID != cur {
		p.invalidate() // the chosen fleet is gone
	}
}

func (p *analyticsPage) filter() (store.Filter, timeRange) {
	r := timeRanges[p.rangeIdx]
	f := store.Filter{FleetID: p.fleetID}
	if r.span > 0 {
		f.From = time.Now().Add(-r.span)
	}
	return f, r
}

// query starts the audit-log reads in a goroutine; pickup applies them.
func (p *analyticsPage) query() {
	f, r := p.filter()
	ctl := p.app.ctl
	started := p.load.start(p.gen, func() (d analyticsData, err error) {
		d.f, d.r = f, r
		if d.tot, err = ctl.Totals(f); err != nil {
			return
		}
		if d.series, err = ctl.CostSeries(f, r.bucket); err != nil {
			return
		}
		if d.agents, err = ctl.AgentStats(f); err != nil {
			return
		}
		d.sessions, err = ctl.SessionStats(f)
		return
	})
	if started {
		p.lastQ, p.dirty = time.Now(), false
	}
}

// pickup applies a finished query on the Qt thread. Results are built in
// locals and only assigned once every call succeeded, so a failure leaves
// the tables' row counts and their data in step.
func (p *analyticsPage) pickup() {
	d, err, ok := p.load.take(p.gen)
	if !ok {
		return
	}
	if err != nil {
		p.err.SetText("Couldn't read the audit log: " + err.Error())
		p.err.Show()
		return
	}
	tot, series, f, r := d.tot, d.series, d.f, d.r
	// The controller owns what it returned; sort copies.
	p.agents = slices.Clone(d.agents)
	p.sessions = slices.Clone(d.sessions)
	if len(p.sessions) > sessionRowLimit {
		p.sessions = p.sessions[:sessionRowLimit] // already newest first
	}
	p.err.Hide()

	p.tiles.spend.set(fmtUSD(tot.CostUSD), r.label)
	p.tiles.turns.set(fmt.Sprint(tot.Results), fmt.Sprintf("%d failed", tot.Results-tot.Successes))
	if tot.Results > 0 {
		p.tiles.success.set(fmt.Sprintf("%.0f%%", tot.SuccessRate()*100), fmt.Sprintf("%d of %d turns", tot.Successes, tot.Results))
	} else {
		p.tiles.success.set("–", "no finished turns")
	}
	p.tiles.tools.set(fmt.Sprint(tot.ToolCalls), "")
	p.tiles.tokens.set(fmtTokens(tot.Usage.Total()), fmtTokens(tot.Usage.CacheRead)+" cache reads")
	if e := tot.OutputPerUSD(); e > 0 {
		p.tiles.eff.set(fmtTokens(int64(e)), "output tokens per $1")
	} else {
		p.tiles.eff.set("–", "")
	}

	cost, out := bucketize(series, f.From, r)
	span := fmt.Sprintf("%s · per %s", r.label, r.unit)
	p.spend.Span, p.out.Span = span, span
	p.spend.Samples, p.out.Samples = max(2, len(cost)), max(2, len(out))
	p.spend.Series = []Series{{Values: cost, Color: 3}}
	p.out.Series = []Series{{Values: out, Color: 0}}
	p.spend.W.Update()
	p.out.W.Update()

	p.sortAgents()
	p.sortSessions()
}

// bucketize turns the sparse series into one value per bucket from the
// range start (or the first point, for all time) to now, filling the gaps
// with zero so the x axis is time.
func bucketize(pts []store.CostPoint, from time.Time, r timeRange) (cost, out []float64) {
	now := time.Now()
	if from.IsZero() {
		if len(pts) == 0 {
			return nil, nil
		}
		from = pts[0].Start
	}
	start := from.Truncate(r.bucket)
	n := int(now.Sub(start)/r.bucket) + 1
	n = min(max(n, 1), 1000)
	if n == 1000 {
		start = now.Truncate(r.bucket).Add(-999 * r.bucket)
	}
	cost = make([]float64, n)
	out = make([]float64, n)
	for _, pt := range pts {
		i := int(pt.Start.Sub(start) / r.bucket)
		if i >= 0 && i < n {
			cost[i] += pt.CostUSD
			out[i] += float64(pt.OutputTokens)
		}
	}
	return cost, out
}

func (p *analyticsPage) agentName(id string) string {
	if p.snap != nil {
		for _, a := range p.snap.Agents {
			if a.ID == id {
				return a.Name
			}
		}
	}
	if id == "" {
		return "(none)"
	}
	return id
}

func pct(n, of int) string {
	if of == 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", float64(n)*100/float64(of))
}

func (p *analyticsPage) agentCell(r, c int) cell {
	if r < 0 || r >= len(p.agents) {
		return cell{}
	}
	s := &p.agents[r]
	switch c {
	case 0:
		return cell{text: p.agentName(s.AgentID)}
	case 1:
		return cell{text: fmt.Sprint(s.Results)}
	case 2:
		cl := cell{text: pct(s.Successes, s.Results)}
		if s.Results > 0 && s.SuccessRate() < 0.8 {
			cl.tone = fleet.ToneWarn
		}
		return cl
	case 3:
		return cell{text: fmt.Sprint(s.ToolCalls)}
	case 4:
		cl := cell{text: fmt.Sprint(s.Errors), alpha: 0.75}
		if s.Errors > 0 {
			cl.tone = fleet.ToneError
		}
		return cl
	case 5:
		return cell{text: fmtTokens(s.Usage.Total())}
	case 6:
		return cell{text: fmtUSD(s.CostUSD)}
	case 7:
		if e := s.OutputPerUSD(); e > 0 {
			return cell{text: fmtTokens(int64(e))}
		}
		return cell{text: "–", alpha: 0.5}
	}
	return cell{}
}

func (p *analyticsPage) sortAgents() {
	col, sign := p.agentT.sortCol, p.agentT.sortSign()
	slices.SortStableFunc(p.agents, func(x, y store.AgentStats) int {
		var c int
		switch col {
		case 0:
			c = cmp.Compare(strings.ToLower(p.agentName(x.AgentID)), strings.ToLower(p.agentName(y.AgentID)))
		case 1:
			c = cmp.Compare(x.Results, y.Results)
		case 2:
			c = cmp.Compare(x.SuccessRate(), y.SuccessRate())
		case 3:
			c = cmp.Compare(x.ToolCalls, y.ToolCalls)
		case 4:
			c = cmp.Compare(x.Errors, y.Errors)
		case 5:
			c = cmp.Compare(x.Usage.Total(), y.Usage.Total())
		case 6:
			c = cmp.Compare(x.CostUSD, y.CostUSD)
		case 7:
			c = cmp.Compare(x.OutputPerUSD(), y.OutputPerUSD())
		case -1: // most expensive first
			return cmp.Compare(y.CostUSD, x.CostUSD)
		default:
			return 0
		}
		return c * sign
	})
	keys := make([]string, len(p.agents))
	for i, s := range p.agents {
		keys[i] = s.AgentID
	}
	p.agentList.update(keys)
}

func (p *analyticsPage) sessionCell(r, c int) cell {
	if r < 0 || r >= len(p.sessions) {
		return cell{}
	}
	s := &p.sessions[r]
	switch c {
	case 0:
		id := s.SessionID
		if len(id) > 13 {
			id = id[:13]
		}
		return cell{text: id, alpha: 0.75}
	case 1:
		return cell{text: p.agentName(s.AgentID)}
	case 2:
		return cell{text: s.First.Local().Format("Jan 2 15:04"), alpha: 0.75}
	case 3:
		return cell{text: fmtDur(s.Last.Sub(s.First))}
	case 4:
		return cell{text: fmt.Sprint(s.Results)}
	case 5:
		return cell{text: pct(s.Successes, s.Results)}
	case 6:
		return cell{text: fmtTokens(s.Usage.Total())}
	case 7:
		return cell{text: fmtUSD(s.CostUSD)}
	}
	return cell{}
}

func (p *analyticsPage) sortSessions() {
	col, sign := p.sessionT.sortCol, p.sessionT.sortSign()
	slices.SortStableFunc(p.sessions, func(x, y store.SessionStats) int {
		var c int
		switch col {
		case 0:
			c = cmp.Compare(x.SessionID, y.SessionID)
		case 1:
			c = cmp.Compare(strings.ToLower(p.agentName(x.AgentID)), strings.ToLower(p.agentName(y.AgentID)))
		case 2:
			c = x.First.Compare(y.First)
		case 3:
			c = cmp.Compare(x.Last.Sub(x.First), y.Last.Sub(y.First))
		case 4:
			c = cmp.Compare(x.Results, y.Results)
		case 5:
			c = cmp.Compare(x.SuccessRate(), y.SuccessRate())
		case 6:
			c = cmp.Compare(x.Usage.Total(), y.Usage.Total())
		case 7:
			c = cmp.Compare(x.CostUSD, y.CostUSD)
		case -1: // newest first
			return y.First.Compare(x.First)
		default:
			return 0
		}
		return c * sign
	})
	keys := make([]string, len(p.sessions))
	for i, s := range p.sessions {
		keys[i] = s.SessionID
	}
	p.sessList.update(keys)
}
