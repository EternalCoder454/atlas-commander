package theme

import (
	"fmt"
	"strings"
)

// Palette maps QPalette role names to hex colours, so the Qt side can build a
// QPalette without knowing the theme's keys. Qt's Fusion style draws parts
// the style sheet does not reach (focus frames, disabled text) from these.
func Palette(t Theme) map[string]string {
	t = t.Filled()
	c := func(k string) rgb { return mustHex(t.Colors[k]) }
	winBg, winFg := c("window_bg_color"), c("window_fg_color")
	return map[string]string{
		"Window":          winBg.hex(),
		"WindowText":      winFg.hex(),
		"Base":            c("view_bg_color").hex(),
		"AlternateBase":   c("card_bg_color").hex(),
		"Text":            c("view_fg_color").hex(),
		"Button":          c("card_bg_color").hex(),
		"ButtonText":      c("card_fg_color").hex(),
		"Highlight":       c("accent_bg_color").hex(),
		"HighlightedText": c("accent_fg_color").hex(),
		"ToolTipBase":     c("popover_bg_color").hex(),
		"ToolTipText":     c("popover_fg_color").hex(),
		"PlaceholderText": over(c("view_fg_color"), c("view_bg_color"), 0.5).hex(),
		"Link":            c("accent_color").hex(),
		"Mid":             over(winFg, winBg, 0.25).hex(),
		"Dark":            over(rgb{}, winBg, 0.3).hex(),
		"Light":           over(rgb{255, 255, 255}, winBg, 0.3).hex(),
	}
}

// Glass is how see-through the window's surfaces are, as opacities from 0 to
// 1. It is Atlas Monitor's scheme: the frame (header and sidebar) is the
// window's own surface, the page is a sheet laid on the frame so its opacity
// compounds with the frame's, and cards on the page keep more colour still.
// Text and charts are never given an alpha.
type Glass struct {
	On                bool
	Frame, Page, Card float64
	// Clear leaves the window's own background transparent even when the
	// surfaces are solid: the window is translucent-capable and rounds its own
	// corners, so what lies outside them must not be painted.
	Clear bool
}

// GlassFor returns the opacities for a transparency level ("off", "subtle",
// "medium", "strong"). Anything else is off, which paints everything solid.
func GlassFor(level string) Glass {
	switch level {
	case "subtle":
		return Glass{On: true, Frame: 0.90, Page: 0.55, Card: 0.85}
	case "medium":
		return Glass{On: true, Frame: 0.78, Page: 0.45, Card: 0.75}
	case "strong":
		return Glass{On: true, Frame: 0.62, Page: 0.40, Card: 0.65}
	}
	return Glass{Frame: 1, Page: 1, Card: 1}
}

// q quotes a font family for a style sheet.
func q(family string) string {
	return `"` + strings.NewReplacer(`"`, "", `\`, "", "\n", "").Replace(family) + `"`
}

// QSS generates the Qt style sheet for Fusion-based widgets. The output
// depends only on the arguments and is stable between calls, so the UI can
// compare strings to skip a pointless re-polish of every widget.
//
// Conventions the UI follows: the sidebar has objectName "sidebar" and the
// page "page"; a button with property accent="true" is the accent variant;
// QFrame[card="true"] is a card; QLabel[status="ok|warn|error|idle"] colours
// text by state, and QLabel[accenttext="true"] uses the accent text colour.
// There are no shadows anywhere, matching the family's flat look.
func QSS(t Theme, m Metrics, uiFont, monoFont string, fontPt int, g Glass) string {
	t = t.Filled()
	c := func(k string) rgb { return mustHex(t.Colors[k]) }
	h := func(k string) string { return t.Colors[k] }

	winFg, winBg := c("window_fg_color"), c("window_bg_color")
	accent := c("accent_bg_color")
	hair := fmt.Sprintf("%dpx solid %s", m.Hairline, h("border_color"))
	hairA := fmt.Sprintf("%dpx solid %s", m.Hairline, winFg.rgba(0.12))
	hover := winFg.rgba(0.06)
	press := winFg.rgba(0.12)
	sel := accent.rgba(0.25)
	r, rs, rp, pad := m.Radius, m.SmallRadius, m.PageRadius, m.Pad

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("/* Atlas Commander theme: %s (%s) */\n", t.ID, map[bool]string{true: "dark", false: "light"}[t.Dark])

	family := ""
	if uiFont != "" {
		family = "font-family: " + q(uiFont) + ";"
	}
	w("QWidget { color: %s; %s font-size: %dpt; }\n", h("window_fg_color"), family, fontPt)
	if g.On || g.Clear {
		// The window is translucent: its own background goes, and the frame
		// and page paint themselves at partial opacity (see Glass).
		w("QMainWindow, QWidget#centralwidget { background-color: transparent; }\n")
	} else {
		w("QMainWindow, QWidget#centralwidget { background-color: %s; }\n", h("window_bg_color"))
	}
	cardBg := h("card_bg_color")
	if g.On {
		cardBg = c("card_bg_color").rgba(g.Card)
	}
	w("QWidget:disabled { color: %s; }\n", winFg.rgba(0.45))
	if monoFont != "" {
		w("*[mono=\"true\"], QPlainTextEdit[mono=\"true\"], QTextEdit[mono=\"true\"] { font-family: %s, monospace; }\n", q(monoFont))
	}

	// Frame layering as in Monitor: the sidebar is the window's own surface and
	// the page is a lighter rounded sheet above it.
	w("QWidget#sidebar { background-color: %s; color: %s; }\n", h("sidebar_bg_color"), h("sidebar_fg_color"))
	w("QWidget#sidebar QLabel { color: %s; background: transparent; }\n", h("sidebar_fg_color"))
	w("QWidget#sidebar QPushButton, QWidget#sidebar QToolButton { background: transparent; color: %s; border: none; border-radius: %dpx; padding: %dpx %dpx; text-align: left; }\n", h("sidebar_fg_color"), rs, pad, pad+2)
	w("QWidget#sidebar QPushButton:hover, QWidget#sidebar QToolButton:hover { background-color: %s; }\n", hover)
	w("QWidget#sidebar QPushButton:checked, QWidget#sidebar QToolButton:checked { background-color: %s; }\n", winFg.rgba(0.08))
	w("QWidget#page { background-color: %s; color: %s; border: %s; border-top-left-radius: %dpx; }\n", h("view_bg_color"), h("view_fg_color"), hairA, rp)

	w("QToolBar { background-color: %s; color: %s; border: none; spacing: %dpx; }\n", h("headerbar_bg_color"), h("headerbar_fg_color"), pad/2)
	w("QStatusBar { background-color: %s; color: %s; border: none; }\n", h("headerbar_bg_color"), h("headerbar_fg_color"))
	w("QFrame[card=\"true\"] { background-color: %s; color: %s; border: %s; border-radius: %dpx; }\n", cardBg, h("card_fg_color"), hair, r)
	w("QFrame[card=\"true\"] QLabel { background: transparent; color: %s; }\n", h("card_fg_color"))
	w("QLabel { background: transparent; }\n")
	w("QLabel[accenttext=\"true\"], QFrame[card=\"true\"] QLabel[accenttext=\"true\"] { color: %s; }\n", h("accent_color"))
	w("QLabel[status=\"ok\"], QFrame[card=\"true\"] QLabel[status=\"ok\"] { color: %s; }\n", h("status_ok"))
	w("QLabel[status=\"warn\"], QFrame[card=\"true\"] QLabel[status=\"warn\"] { color: %s; }\n", h("status_warn"))
	w("QLabel[status=\"error\"], QFrame[card=\"true\"] QLabel[status=\"error\"] { color: %s; }\n", h("status_error"))
	w("QLabel[status=\"idle\"], QFrame[card=\"true\"] QLabel[status=\"idle\"] { color: %s; }\n", h("status_idle"))
	w("QLabel[status=\"success\"], QFrame[card=\"true\"] QLabel[status=\"success\"] { color: %s; }\n", h("success_color"))
	w("QLabel[status=\"warning\"], QFrame[card=\"true\"] QLabel[status=\"warning\"] { color: %s; }\n", h("warning_color"))
	w("QLabel[status=\"danger\"], QFrame[card=\"true\"] QLabel[status=\"danger\"] { color: %s; }\n", h("error_color"))
	w("QLabel[caption=\"true\"] { color: %s; }\n", winFg.rgba(0.62))
	w("QFrame[card=\"true\"] QLabel[caption=\"true\"] { color: %s; }\n", c("card_fg_color").rgba(0.62))
	w("QLabel[heading=\"true\"] { font-size: %.1fpt; font-weight: 600; }\n", float64(fontPt)*1.15)
	w("QScrollArea[page=\"true\"], QScrollArea[page=\"true\"] > QWidget > QWidget { background: transparent; border: none; }\n")
	w("QFrame[hairline=\"true\"] { background-color: %s; border: none; max-height: %dpx; }\n", h("border_color"), m.Hairline)
	w("QFrame[chart=\"1\"] { color: %s; } QFrame[chart=\"2\"] { color: %s; } QFrame[chart=\"3\"] { color: %s; } QFrame[chart=\"4\"] { color: %s; } QFrame[chart=\"5\"] { color: %s; }\n",
		h("chart_1"), h("chart_2"), h("chart_3"), h("chart_4"), h("chart_5"))

	// Tables: Monitor's look. They sit on the page colour (no inset well), no
	// zebra stripes, selection is the accent at low alpha so the text colour
	// stays readable, and header labels are quiet: normal weight at 75 %.
	// Commander's own tables paint their cells and lines in a delegate; these
	// rules cover the stock views.
	w("QTableView, QTreeView, QListView { background-color: transparent; alternate-background-color: transparent; color: %s; border: none; gridline-color: %s; selection-background-color: %s; selection-color: %s; outline: 0; }\n",
		h("window_fg_color"), h("grid_color"), sel, h("window_fg_color"))
	w("QTableView::item, QTreeView::item, QListView::item { min-height: %dpx; padding: 0 %dpx; border: none; }\n", m.RowHeight, pad)
	w("QTableView::item:selected, QTreeView::item:selected, QListView::item:selected { background-color: %s; color: %s; }\n", sel, h("window_fg_color"))
	w("QTableView::item:hover, QTreeView::item:hover, QListView::item:hover { background-color: %s; }\n", hover)
	w("QTableView[painted=\"true\"]::item, QTableView[painted=\"true\"]::item:hover, QTableView[painted=\"true\"]::item:selected { background: transparent; }\n")
	w("QHeaderView { background-color: transparent; border: none; }\n")
	w("QHeaderView::section { background-color: transparent; color: %s; border: none; border-bottom: %s; border-right: %s; padding: 10px 10px 8px 10px; font-weight: normal; }\n", winFg.rgba(0.75), hair, hair)
	w("QHeaderView::section:last { border-right: none; }\n")
	w("QTableCornerButton::section { background-color: transparent; border: none; }\n")
	w("QLabel[title=\"true\"] { font-size: %.1fpt; font-weight: 600; }\n", float64(fontPt)*1.45)
	w("QLabel[figure=\"true\"] { font-size: %.1fpt; }\n", float64(fontPt)*1.6)
	w("QWidget#header QToolButton { background-color: transparent; min-height: 0; padding: 4px 8px; }\n")
	w("QWidget#header QToolButton:hover { background-color: %s; }\n", winFg.rgba(0.08))
	// Monitor's command bar: flat commands, an icon and a word each, filled
	// only on hover.
	w("QPushButton[command=\"true\"] { background-color: transparent; padding: %dpx %dpx; }\n", pad/2+1, pad+2)
	w("QPushButton[command=\"true\"]:hover { background-color: %s; }\n", winFg.rgba(0.08))
	w("QPushButton[command=\"true\"]:pressed { background-color: %s; }\n", winFg.rgba(0.12))
	w("QPushButton[command=\"true\"]:disabled { background-color: transparent; }\n")
	w("QPushButton[command=\"true\"][danger=\"true\"] { background-color: transparent; }\n")
	w("QPushButton[command=\"true\"][danger=\"true\"]:hover { background-color: %s; }\n", c("error_color").rgba(0.15))

	// Buttons are flat; the accent variant is the only filled one.
	// A transparent border the focus ring can colour in, so a button doesn't
	// grow by two pixels and shift its neighbours when it takes focus.
	w("QPushButton, QToolButton { background-color: %s; color: %s; border: 1px solid transparent; border-radius: %dpx; padding: %dpx %dpx; min-height: %dpx; }\n", winFg.rgba(0.08), h("window_fg_color"), rs, pad/2, pad*2, 2*pad+8)
	w("QPushButton:hover, QToolButton:hover { background-color: %s; }\n", winFg.rgba(0.12))
	w("QPushButton:pressed, QToolButton:pressed { background-color: %s; }\n", press)
	w("QPushButton:disabled, QToolButton:disabled { background-color: %s; color: %s; }\n", winFg.rgba(0.04), winFg.rgba(0.4))
	w("QPushButton:focus, QToolButton:focus { border: 1px solid %s; }\n", h("accent_bg_color"))
	w("QPushButton[accent=\"true\"] { background-color: %s; color: %s; }\n", h("accent_bg_color"), h("accent_fg_color"))
	w("QPushButton[accent=\"true\"]:hover { background-color: %s; }\n", over(mustHex(h("accent_fg_color")), accent, 0.12).hex())
	w("QPushButton[accent=\"true\"]:pressed { background-color: %s; }\n", over(mustHex(h("accent_fg_color")), accent, 0.2).hex())
	w("QPushButton[accent=\"true\"]:disabled { background-color: %s; color: %s; }\n", accent.rgba(0.4), mustHex(h("accent_fg_color")).rgba(0.7))
	// A choice button ("Use system setting") while it is the choice: in the
	// accent, as the ring round a chosen theme's circle is.
	w("QPushButton[choice=\"true\"][chosen=\"true\"] { background-color: %s; color: %s; }\n", h("accent_bg_color"), h("accent_fg_color"))
	w("QPushButton[danger=\"true\"] { background-color: %s; color: %s; }\n", c("error_color").rgba(0.15), h("error_color"))
	w("QPushButton[danger=\"true\"]:hover { background-color: %s; }\n", c("error_color").rgba(0.25))
	w("QPushButton[danger=\"true\"]:disabled { background-color: %s; color: %s; }\n", winFg.rgba(0.04), winFg.rgba(0.4))

	// Inputs.
	in := fmt.Sprintf("background-color: %s; color: %s; border: %s; border-radius: %dpx; padding: %dpx %dpx; selection-background-color: %s; selection-color: %s;",
		winFg.rgba(0.04), h("window_fg_color"), hairA, rs, pad/2, pad, accent.rgba(0.35), h("window_fg_color"))
	w("QLineEdit, QTextEdit, QPlainTextEdit, QComboBox:editable, QSpinBox, QDoubleSpinBox { %s }\n", in)
	w("QLineEdit:focus, QTextEdit:focus, QPlainTextEdit:focus, QComboBox:editable:focus, QSpinBox:focus, QDoubleSpinBox:focus { border: 1px solid %s; }\n", h("accent_bg_color"))
	w("QLineEdit:disabled, QTextEdit:disabled, QPlainTextEdit:disabled, QComboBox:disabled, QSpinBox:disabled { color: %s; }\n", winFg.rgba(0.4))
	// Monitor's dropdown: no box, just the choice and a chevron, with a faint
	// fill on hover. An editable one keeps the input look above.
	w("QComboBox { background-color: transparent; color: %s; border: 1px solid transparent; border-radius: %dpx; padding: %dpx %dpx %dpx %dpx; }\n", h("window_fg_color"), rs, pad/2, pad/2+2, pad/2, pad)
	w("QComboBox:hover:!editable, QComboBox:on:!editable { background-color: %s; }\n", hover)
	w("QComboBox:focus:!editable { border: 1px solid %s; }\n", h("accent_bg_color"))
	w("QComboBox::drop-down { border: none; width: %dpx; subcontrol-origin: padding; subcontrol-position: center right; }\n", pad*2+4)
	if IndicatorDir != "" {
		w("QComboBox::down-arrow { image: url(%s); width: 16px; height: 16px; }\n", chevronPath(h("window_fg_color")))
	}
	w("QComboBox QAbstractItemView { background-color: %s; color: %s; border: %s; selection-background-color: %s; selection-color: %s; outline: 0; }\n",
		h("popover_bg_color"), h("popover_fg_color"), hair, sel, h("popover_fg_color"))
	w("QSpinBox::up-button, QSpinBox::down-button, QDoubleSpinBox::up-button, QDoubleSpinBox::down-button { border: none; width: %dpx; background: transparent; }\n", pad*2)
	w("QCheckBox, QRadioButton { spacing: %dpx; background: transparent; }\n", pad)
	// Indicators: a rounded box that fills with the accent and shows a tick.
	// Every state is styled, since styling only some leaves the rest blank.
	w("QCheckBox::indicator, QRadioButton::indicator { width: 16px; height: 16px; border: 1px solid %s; background-color: %s; }\n", winFg.rgba(0.38), winFg.rgba(0.04))
	w("QCheckBox::indicator { border-radius: 4px; } QRadioButton::indicator { border-radius: 9px; }\n")
	w("QCheckBox::indicator:hover, QRadioButton::indicator:hover { border-color: %s; }\n", h("accent_bg_color"))
	w("QCheckBox::indicator:checked, QRadioButton::indicator:checked { background-color: %s; border-color: %s; }\n", h("accent_bg_color"), h("accent_bg_color"))
	if IndicatorDir != "" {
		w("QCheckBox::indicator:checked { image: url(%s); }\n", checkPath(h("accent_fg_color")))
	}
	w("QCheckBox::indicator:disabled, QRadioButton::indicator:disabled { border-color: %s; background-color: transparent; }\n", winFg.rgba(0.15))
	w("QCheckBox::indicator:checked:disabled, QRadioButton::indicator:checked:disabled { background-color: %s; border-color: transparent; }\n", accent.rgba(0.4))
	w("QDialogButtonBox { dialogbuttonbox-buttons-have-icons: 0; }\n")

	// Scroll bars: thin, no arrows, a handle that only shows as a faint bar.
	w("QScrollBar:vertical { background: transparent; width: 10px; margin: 0; }\n")
	w("QScrollBar:horizontal { background: transparent; height: 10px; margin: 0; }\n")
	w("QScrollBar::handle { background-color: %s; border-radius: 3px; min-height: 28px; min-width: 28px; margin: 2px; }\n", winFg.rgba(0.25))
	w("QScrollBar::handle:hover { background-color: %s; }\n", winFg.rgba(0.4))
	w("QScrollBar::add-line, QScrollBar::sub-line { width: 0; height: 0; background: none; border: none; }\n")
	w("QScrollBar::add-page, QScrollBar::sub-page { background: transparent; }\n")

	// Popups.
	w("QMenu { background-color: %s; color: %s; border: %s; border-radius: %dpx; padding: 4px; }\n", h("popover_bg_color"), h("popover_fg_color"), hair, rs+2)
	w("QMenu::item { padding: %dpx %dpx; border-radius: %dpx; }\n", pad/2+2, pad*3, rs)
	w("QMenu::item:selected { background-color: %s; color: %s; }\n", winFg.rgba(0.08), h("popover_fg_color"))
	w("QMenu::item:disabled { color: %s; }\n", mustHex(h("popover_fg_color")).rgba(0.4))
	w("QMenu::separator { height: 1px; background-color: %s; margin: 4px 6px; }\n", h("border_color"))
	w("QToolTip { background-color: %s; color: %s; border: %s; padding: 4px 6px; }\n", h("popover_bg_color"), h("popover_fg_color"), hair)
	w("QDialog, QMessageBox { background-color: %s; color: %s; }\n", h("dialog_bg_color"), h("dialog_fg_color"))
	w("QDialog QLabel, QMessageBox QLabel { color: %s; }\n", h("dialog_fg_color"))

	// Tabs.
	w("QTabWidget::pane { border: none; }\n")
	w("QTabBar::tab { background: transparent; color: %s; padding: %dpx %dpx; border: none; border-bottom: 2px solid transparent; }\n", winFg.rgba(0.62), pad, pad*2)
	w("QTabBar::tab:hover { color: %s; }\n", h("window_fg_color"))
	w("QTabBar::tab:selected { color: %s; border-bottom: 2px solid %s; }\n", h("accent_color"), h("accent_color"))

	w("QProgressBar { background-color: %s; border: none; border-radius: %dpx; max-height: 8px; text-align: center; }\n", winFg.rgba(0.14), rs)
	w("QProgressBar::chunk { background-color: %s; border-radius: %dpx; }\n", h("accent_bg_color"), rs)
	w("QSplitter::handle { background-color: %s; }\n", winBg.hex())
	return b.String()
}
