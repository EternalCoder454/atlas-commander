package ui

import (
	"fmt"
	"os"
	"slices"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"atlas-commander/internal/paths"
	"atlas-commander/internal/theme"
)

// themeGroups are the editor's sections over theme.BaseKeys, with names a
// person would use.
var themeGroups = []struct {
	title string
	keys  [][2]string // key, label
}{
	{"Surfaces", [][2]string{
		{"sidebar_bg_color", "Frame"}, {"sidebar_fg_color", "Frame text"},
		{"window_bg_color", "Page"}, {"window_fg_color", "Page text"},
		{"view_bg_color", "Lists and editors"}, {"view_fg_color", "List text"},
		{"card_bg_color", "Cards"}, {"card_fg_color", "Card text"},
		{"headerbar_bg_color", "Header"}, {"headerbar_fg_color", "Header text"},
		{"dialog_bg_color", "Dialogs"}, {"dialog_fg_color", "Dialog text"},
		{"popover_bg_color", "Menus"}, {"popover_fg_color", "Menu text"},
	}},
	{"Accent", [][2]string{
		{"accent_bg_color", "Accent fill"}, {"accent_fg_color", "Text on accent"},
		{"accent_color", "Accent text"},
	}},
	{"Status", [][2]string{
		{"success_color", "Success"}, {"warning_color", "Warning"}, {"error_color", "Error"},
	}},
}

// editTheme opens the theme editor on a copy of the current theme. Every
// change previews on the whole window; Cancel puts the saved theme back.
func (p *settingsPage) editTheme() {
	a := p.app
	src := theme.Find(a.themes, a.settings.Theme, a.systemDark()).Filled()
	colors := map[string]string{}
	for _, k := range theme.BaseKeys {
		colors[k] = src.Colors[k]
	}
	work := theme.Theme{Name: src.Name, Summary: "Made in Atlas Commander", Colors: colors}
	if theme.IsBuiltin(src.ID) {
		work.Name = src.Name + " (mine)"
	} else {
		work.Summary = src.Summary
	}

	dlg := qt.NewQDialog(a.win.QWidget)
	defer dlg.DeleteLater()
	dlg.SetWindowTitle("Customize theme")
	dlg.Resize(520, 720)
	outer := qt.NewQVBoxLayout(dlg.QWidget)
	outer.SetContentsMargins(18, 18, 18, 14)
	outer.SetSpacing(10)

	title := pageTitle("Customize theme")
	outer.AddWidget(title.QWidget)
	intro := caption("Every change shows on the window behind this one. Nothing is kept until you save.")
	intro.SetWordWrap(true)
	outer.AddWidget(intro.QWidget)

	nameRow := qt.NewQHBoxLayout2()
	nameRow.AddWidget(qt.NewQLabel3("Name").QWidget)
	name := qt.NewQLineEdit2()
	name.SetText(work.Name)
	nameRow.AddWidget(name.QWidget)
	outer.AddLayout(nameRow.QLayout)

	scroll := qt.NewQScrollArea2()
	scroll.SetWidgetResizable(true)
	scroll.SetFrameShape(qt.QFrame__NoFrame)
	setProp(scroll.QWidget, "page", true)
	body := qt.NewQWidget2()
	col := qt.NewQVBoxLayout(body)
	col.SetContentsMargins(0, 0, 6, 0)
	col.SetSpacing(8)
	scroll.SetWidget(body)
	outer.AddWidget2(scroll.QWidget, 1)

	verdict := qt.NewQLabel2()
	verdict.SetWordWrap(true)
	outer.AddWidget(verdict.QWidget)

	buttons := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Ok | qt.QDialogButtonBox__Cancel)
	save := buttons.Button(qt.QDialogButtonBox__Ok)
	save.SetText("Save theme")
	setProp(save.QWidget, "accent", true)
	outer.AddWidget(buttons.QWidget)

	current := func() theme.Theme {
		t := work
		t.Colors = map[string]string{}
		for k, v := range colors {
			t.Colors[k] = v
		}
		t.Name = strings.TrimSpace(name.Text())
		t.ID = theme.Slug(t.Name)
		t.Primary = t.Colors["window_bg_color"]
		t.Secondary = t.Colors["accent_bg_color"]
		t.Dark = theme.IsDark(t.Primary)
		return t
	}
	check := func() error {
		t := current()
		switch {
		case t.ID == "":
			return fmt.Errorf("give the theme a name")
		case theme.IsBuiltin(t.ID):
			return fmt.Errorf("“%s” is a built-in theme's name; pick another", t.Name)
		case t.ID != src.ID && slices.ContainsFunc(a.themes, func(o theme.Theme) bool { return o.ID == t.ID }):
			// Saving would overwrite that theme's file. Editing a theme of
			// your own under its own name is fine.
			return fmt.Errorf("you already have a theme called “%s”; pick another name", t.Name)
		}
		return theme.Validate(t)
	}
	update := func() {
		a.previewTheme(current())
		if err := check(); err != nil {
			verdict.SetText("●  " + upperFirst(err.Error()))
			setProp(verdict.QWidget, "status", "warning")
			save.SetEnabled(false)
		} else {
			verdict.SetText("●  Readable everywhere: every text colour passes its contrast check.")
			setProp(verdict.QWidget, "status", "success")
			save.SetEnabled(true)
		}
	}

	for _, grp := range themeGroups {
		h := qt.NewQLabel3(grp.title)
		setProp(h.QWidget, "heading", true)
		col.AddSpacing(6)
		col.AddWidget(h.QWidget)
		card := qt.NewQFrame2()
		setProp(card.QWidget, "card", true)
		grid := qt.NewQGridLayout(card.QWidget)
		grid.SetContentsMargins(14, 10, 14, 10)
		grid.SetHorizontalSpacing(12)
		grid.SetVerticalSpacing(6)
		for i, kl := range grp.keys {
			key := kl[0]
			btn := qt.NewQPushButton3("")
			setProp(btn.QWidget, "mono", true)
			btn.SetMinimumWidth(120)
			show := func() {
				btn.SetText(colors[key])
				icon := swatchIcon(colors[key])
				btn.SetIcon(icon)
				icon.Delete()
			}
			show()
			btn.OnClicked(func() {
				init := parseHex(colors[key]).q(1)
				c := qt.QColorDialog_GetColor3(init, dlg.QWidget, kl[1])
				init.Delete()
				if !c.IsValid() {
					return
				}
				colors[key] = strings.ToLower(c.Name())
				show()
				update()
			})
			r, cc := i/2, (i%2)*2
			grid.AddWidget2(qt.NewQLabel3(kl[1]).QWidget, r, cc)
			grid.AddWidget2(btn.QWidget, r, cc+1)
		}
		col.AddWidget(card.QWidget)
	}
	col.AddStretch()

	name.OnTextChanged(func(string) { update() })
	buttons.OnRejected(dlg.Reject)
	buttons.OnAccepted(func() {
		if check() != nil {
			return
		}
		t := current()
		if err := os.MkdirAll(paths.Themes(), 0o700); err != nil {
			a.report(fmt.Errorf("Couldn't save the theme: %w", err))
			return
		}
		if err := theme.Save(paths.Themes(), t); err != nil {
			a.report(fmt.Errorf("Couldn't save the theme: %w", err))
			return
		}
		a.loadThemes()
		a.settings.Theme = t.ID
		// The theme carries its own accent now; an override would hide it.
		a.settings.Accent = ""
		dlg.Accept()
	})
	update()
	// A desktop light/dark switch while the editor is open must not replace
	// its preview.
	a.previewing = true
	dlg.Exec()
	a.previewing = false
	// Saved or not, go back to what the settings say.
	p.fillGallery()
	p.retheme()
}

// swatchIcon is a small rounded square of one colour for a button.
func swatchIcon(hex string) *qt.QIcon {
	const s = 16
	pm := qt.NewQPixmap2(s, s)
	clear := qt.NewQColor2(qt.Transparent)
	pm.FillWithFillColor(clear)
	clear.Delete()
	p := qt.NewQPainter2(pm.QPaintDevice)
	p.SetRenderHint(qt.QPainter__Antialiasing)
	fillRound(p, 0.5, 0.5, s-1, s-1, 4, parseHex(hex), 1)
	strokeRound(p, 0.5, 0.5, s-1, s-1, 4, rgb{128, 128, 128}, 0.5, 1)
	p.End()
	p.Delete()
	icon := qt.NewQIcon()
	icon.AddPixmap(pm)
	pm.Delete()
	return icon
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
