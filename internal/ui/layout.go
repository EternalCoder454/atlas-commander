package ui

// The Simple layout keeps only Board, Approvals and Settings in the sidebar;
// Advanced shows everything. Switching is live: applyLayout rebuilds the
// sidebar rows and tells the board, with no restart.

// simpleHidden are the pages Simple does not offer. An agent's detail page is
// not listed: opening one from a card or the table is fine in either layout.
var simpleHidden = map[string]bool{
	"tasks": true, "fleets": true, "analytics": true, "audit": true, "observed": true,
}

// layoutShows reports whether the page id is reachable in the current layout.
func (a *App) layoutShows(id string) bool {
	return !(a.simpleLayout() && simpleHidden[id])
}

// layoutNav filters the sidebar rows for the current layout. Simple drops the
// group labels as well, since three rows need no headings.
func (a *App) layoutNav(all []navItem) []navItem {
	if !a.simpleLayout() {
		return all
	}
	out := make([]navItem, 0, len(all))
	for _, it := range all {
		if !it.group && a.layoutShows(it.id) {
			out = append(out, it)
		}
	}
	return out
}

// applyLayout makes the window match settings.Layout. It runs once after the
// window is built and again whenever the setting changes.
func (a *App) applyLayout() {
	a.side.setItems(a.navItems())
	if b, ok := a.pages["board"].(*boardPage); ok {
		b.applyLayoutMode(a.simpleLayout())
	}
	// A page Simple hides must not stay on screen after switching to it.
	if !a.layoutShows(a.current) {
		a.side.select_("board")
	}
}
