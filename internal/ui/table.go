package ui

import (
	"slices"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/fleet"
)

// cell is what a dataTable column draws for one row.
type cell struct {
	text  string
	alpha float64 // 0 means fully opaque
	// dot draws a 12 px status dot before the text in that tone.
	dot     bool
	dotTone fleet.Tone
	// tone colours the text: ToneIdle is the normal foreground.
	tone fleet.Tone
}

type tableCol struct {
	title string
	width int // 0 stretches
	mono  bool
	right bool
}

// dataTable is the painted table every list view uses: the board's look
// (no zebra, row hover, accent selection, row lines and dividers) over rows
// the page keeps in a Go slice. The page tells it how many rows there are
// and what each cell shows; the table never copies the data.
type dataTable struct {
	app  *App
	T    *qt.QTableView
	cols []tableCol

	n    int
	keys []string
	cell func(r, c int) cell
	tip  func(r, c int) string

	selected string
	hover    int
	// updating is set while update re-selects after a model reset, so the
	// selection model's own signal does not report a change the page already
	// knows about.
	updating bool

	// sortCol is -1 until the user clicks a header; the page sorts its rows
	// in onSort and calls update.
	sortCol  int
	sortDesc bool
	onSort   func()

	onSelect   func(key string)
	onActivate func(key string)

	model    *qt.QAbstractTableModel
	delegate *qt.QStyledItemDelegate
	ret      *qt.QVariant
	blank    *qt.QVariant
	size     *qt.QSize
	monoF    *qt.QFont
}

func newDataTable(a *App, cols []tableCol, cellFn func(r, c int) cell) *dataTable {
	d := &dataTable{app: a, cols: cols, cell: cellFn, hover: -1, sortCol: -1, blank: qt.NewQVariant()}
	d.T = qt.NewQTableView2()
	t := d.T
	t.SetShowGrid(false)
	t.SetSelectionBehavior(qt.QAbstractItemView__SelectRows)
	t.SetSelectionMode(qt.QAbstractItemView__SingleSelection)
	t.SetEditTriggers(qt.QAbstractItemView__NoEditTriggers)
	t.SetVerticalScrollMode(qt.QAbstractItemView__ScrollPerPixel)
	t.SetHorizontalScrollMode(qt.QAbstractItemView__ScrollPerPixel)
	t.SetMouseTracking(true)
	t.SetWordWrap(false)
	setProp(t.QWidget, "painted", true)
	t.VerticalHeader().Hide()
	t.OnMouseMoveEvent(func(super func(*qt.QMouseEvent), e *qt.QMouseEvent) {
		if row := t.IndexAt(e.Pos()).Row(); row != d.hover {
			d.hover = row
			t.Viewport().Update()
		}
		super(e)
	})
	t.OnLeaveEvent(func(super func(*qt.QEvent), e *qt.QEvent) {
		d.hover = -1
		t.Viewport().Update()
		super(e)
	})

	m := qt.NewQAbstractTableModel()
	d.model = m
	m.OnRowCount(func(parent *qt.QModelIndex) int {
		if parent != nil && parent.IsValid() {
			return 0
		}
		return d.n
	})
	m.OnColumnCount(func(parent *qt.QModelIndex) int {
		if parent != nil && parent.IsValid() {
			return 0
		}
		return len(d.cols)
	})
	m.OnData(func(idx *qt.QModelIndex, role int) *qt.QVariant {
		r, c := idx.Row(), idx.Column()
		if r < 0 || r >= d.n || c < 0 || c >= len(d.cols) {
			return d.blank
		}
		switch qt.ItemDataRole(role) {
		case qt.DisplayRole:
			return d.give(qt.NewQVariant11(d.cell(r, c).text))
		case qt.ToolTipRole:
			if d.tip != nil {
				if s := d.tip(r, c); s != "" {
					return d.give(qt.NewQVariant11(s))
				}
			}
		}
		return d.blank
	})
	m.OnHeaderData(func(super func(int, qt.Orientation, int) *qt.QVariant, section int, o qt.Orientation, role int) *qt.QVariant {
		if o != qt.Horizontal || section < 0 || section >= len(d.cols) {
			return d.blank
		}
		switch qt.ItemDataRole(role) {
		case qt.DisplayRole:
			return d.give(qt.NewQVariant11(d.cols[section].title))
		case qt.TextAlignmentRole:
			al := qt.AlignLeft | qt.AlignVCenter
			if d.cols[section].right {
				al = qt.AlignRight | qt.AlignVCenter
			}
			return d.give(qt.NewQVariant4(int(al)))
		}
		return d.blank
	})
	m.OnFlags(func(super func(*qt.QModelIndex) qt.ItemFlag, idx *qt.QModelIndex) qt.ItemFlag {
		return qt.ItemIsEnabled | qt.ItemIsSelectable
	})
	t.SetModel(m.QAbstractItemModel)

	d.delegate = qt.NewQStyledItemDelegate()
	d.delegate.OnPaint(func(super func(*qt.QPainter, *qt.QStyleOptionViewItem, *qt.QModelIndex), p *qt.QPainter, opt *qt.QStyleOptionViewItem, idx *qt.QModelIndex) {
		d.paint(p, opt, idx)
	})
	d.delegate.OnSizeHint(func(super func(*qt.QStyleOptionViewItem, *qt.QModelIndex) *qt.QSize, opt *qt.QStyleOptionViewItem, idx *qt.QModelIndex) *qt.QSize {
		w := 80
		if c := idx.Column(); c >= 0 && c < len(d.cols) && d.cols[c].width > 0 {
			w = d.cols[c].width
		}
		if d.size != nil {
			d.size.Delete()
		}
		d.size = qt.NewQSize2(w, d.app.metrics.RowHeight)
		return d.size
	})
	t.SetItemDelegate(d.delegate.QAbstractItemDelegate)

	h := t.HorizontalHeader()
	h.SetHighlightSections(false)
	h.SetSectionsClickable(true)
	h.SetMinimumSectionSize(40)
	for i, c := range cols {
		if c.width == 0 {
			h.SetSectionResizeMode2(i, qt.QHeaderView__Stretch)
		} else {
			h.SetSectionResizeMode2(i, qt.QHeaderView__Interactive)
			t.SetColumnWidth(i, c.width)
		}
	}
	h.OnSectionClicked(func(i int) {
		if d.onSort == nil {
			return
		}
		if i == d.sortCol {
			d.sortDesc = !d.sortDesc
		} else {
			d.sortCol, d.sortDesc = i, false
		}
		order := qt.AscendingOrder
		if d.sortDesc {
			order = qt.DescendingOrder
		}
		h.SetSortIndicatorShown(true)
		h.SetSortIndicator(i, order)
		d.onSort()
		t.ScrollToTop()
	})

	t.SelectionModel().OnCurrentRowChanged(func(cur, prev *qt.QModelIndex) {
		if d.updating {
			return
		}
		key := ""
		if cur.IsValid() && cur.Row() < len(d.keys) {
			key = d.keys[cur.Row()]
		}
		if key != d.selected {
			d.selected = key
			if d.onSelect != nil {
				d.onSelect(key)
			}
		}
	})
	t.OnDoubleClicked(func(idx *qt.QModelIndex) {
		if d.onActivate != nil && idx.IsValid() && idx.Row() < len(d.keys) {
			d.onActivate(d.keys[idx.Row()])
		}
	})

	a.themed = append(a.themed, d.themeChanged)
	d.themeChanged()
	return d
}

func (d *dataTable) give(v *qt.QVariant) *qt.QVariant {
	if d.ret != nil {
		d.ret.Delete()
	}
	d.ret = v
	return v
}

func (d *dataTable) themeChanged() {
	if d.monoF != nil {
		d.monoF.Delete()
	}
	d.monoF = d.app.monoFont(1)
	d.T.VerticalHeader().SetDefaultSectionSize(d.app.metrics.RowHeight)
	d.T.Viewport().Update()
}

// update tells the table the page's rows changed. keys identify the rows in
// their new order; when they are the same as before only the cells repaint,
// otherwise the model resets and the selection follows its key.
func (d *dataTable) update(keys []string) {
	if slices.Equal(keys, d.keys) {
		if len(keys) > 0 {
			tl := d.model.CreateIndex(0, 0)
			br := d.model.CreateIndex(len(keys)-1, len(d.cols)-1)
			d.model.DataChanged(&tl, &br)
		}
		return
	}
	sel := d.selected
	d.model.BeginResetModel()
	d.keys = slices.Clone(keys)
	d.n = len(keys)
	d.model.EndResetModel()
	d.selected = ""
	if i := slices.Index(d.keys, sel); i >= 0 {
		// The reset cleared the view's selection; restore it without
		// reporting it and without the view scrolling to the row, because
		// the key did not change and the user may be reading elsewhere.
		d.updating = true
		d.T.SetAutoScroll(false)
		d.T.SelectRow(i)
		d.T.SetAutoScroll(true)
		d.updating = false
		d.selected = sel
	}
	if d.selected != sel && d.onSelect != nil {
		d.onSelect(d.selected)
	}
}

// selectKey selects the row with key, if there is one.
func (d *dataTable) selectKey(key string) {
	if i := slices.Index(d.keys, key); i >= 0 {
		d.T.SelectRow(i)
	}
}

// sortSign is 1, or -1 when the user asked for descending order.
func (d *dataTable) sortSign() int {
	if d.sortDesc {
		return -1
	}
	return 1
}

func (d *dataTable) paint(p *qt.QPainter, opt *qt.QStyleOptionViewItem, idx *qt.QModelIndex) {
	r, c := idx.Row(), idx.Column()
	if r < 0 || r >= d.n || c < 0 || c >= len(d.cols) {
		return
	}
	col := d.cols[c]
	cl := d.cell(r, c)
	pal := d.app.pal
	rect := opt.Rect()
	x, y, w, h := rect.X(), rect.Y(), rect.Width(), rect.Height()

	p.Save()
	defer p.Restore()

	switch {
	case opt.State()&qt.QStyle__State_Selected != 0:
		fill := pal.accentB.q(0.25)
		p.FillRect5(x, y, w, h, fill)
		fill.Delete()
	case r == d.hover:
		fill := pal.fg.q(0.05)
		p.FillRect5(x, y, w, h, fill)
		fill.Delete()
	}
	line := pal.rowline.q(1)
	p.FillRect5(x, y+h-1, w, 1, line)
	line.Delete()
	if c < len(d.cols)-1 {
		div := pal.divider.q(1)
		p.FillRect5(x+w-1, y, 1, h, div)
		div.Delete()
	}

	const padX = 10
	tx, tw := x+padX, w-2*padX
	if cl.dot {
		dc := pal.toneColor(cl.dotTone).q(1)
		br := qt.NewQBrush3(dc)
		p.SetRenderHint(qt.QPainter__Antialiasing)
		p.SetPenWithStyle(qt.NoPen)
		p.SetBrush(br)
		p.DrawEllipse(rectf(float64(tx), float64(y+(h-12)/2), 12, 12))
		br.Delete()
		dc.Delete()
		tx += 22
		tw -= 22
	}
	if cl.text == "" || tw <= 0 {
		return
	}
	var fm *qt.QFontMetrics
	if col.mono {
		p.SetFont(d.monoF)
		fm = qt.NewQFontMetrics(d.monoF)
	} else {
		f := d.T.Font()
		p.SetFont(f)
		fm = qt.NewQFontMetrics(f)
	}
	defer fm.Delete()
	alpha := cl.alpha
	if alpha == 0 {
		alpha = 1
	}
	var tc *qt.QColor
	if cl.tone == fleet.ToneIdle {
		tc = pal.fg.q(alpha)
	} else {
		tc = pal.toneColor(cl.tone).q(alpha)
	}
	p.SetPen(tc)
	tc.Delete()
	align := qt.AlignLeft | qt.AlignVCenter
	if col.right {
		align = qt.AlignRight | qt.AlignVCenter
	}
	p.DrawText7(tx, y, tw, h, int(align), fm.ElidedText(firstLine(cl.text), qt.ElideRight, tw))
}

// emptyState is what a list shows when it has no rows: the Atlas mark,
// faint, over a heading and a line saying how rows get there.
func emptyState(title, hint string) *qt.QWidget {
	w := qt.NewQWidget2()
	l := qt.NewQVBoxLayout(w)
	l.SetSpacing(6)
	l.AddStretch()
	l.AddWidget3(newFaintMark(56, 0.22), 0, qt.AlignHCenter)
	l.AddSpacing(10)
	t := qt.NewQLabel3(title)
	setProp(t.QWidget, "heading", true)
	t.SetAlignment(qt.AlignCenter)
	l.AddWidget(t.QWidget)
	if hint != "" {
		h := caption(hint)
		h.SetAlignment(qt.AlignCenter)
		h.SetWordWrap(true)
		// A wrapping label placed with an alignment is laid out from its
		// one-line size hint and clipped, so its size is fixed here.
		h.SetFixedWidth(440)
		h.EnsurePolished()
		h.SetMinimumHeight(h.HeightForWidth(440) + 2)
		l.AddWidget3(h.QWidget, 0, qt.AlignHCenter)
	}
	l.AddStretch()
	l.AddStretch()
	return w
}

// listView stacks a table over its empty state and shows whichever fits.
type listView struct {
	W     *qt.QStackedWidget
	empty *qt.QWidget
	table *dataTable
}

func newListView(t *dataTable, emptyTitle, emptyHint string) *listView {
	v := &listView{W: qt.NewQStackedWidget2(), empty: emptyState(emptyTitle, emptyHint), table: t}
	v.W.AddWidget(v.empty)
	v.W.AddWidget(t.T.QWidget)
	return v
}

func (v *listView) update(keys []string) {
	v.table.update(keys)
	if len(keys) == 0 {
		v.W.SetCurrentWidget(v.empty)
	} else {
		v.W.SetCurrentWidget(v.table.T.QWidget)
	}
}
