package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
	"lazyssh/internal/search"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
	"lazyssh/internal/vterm"
)

// The workspace runs several terminal sessions in one screen. It has two
// keyboard modes (like tuimux): TERMINAL sends every key to the focused
// session; CONTROL (toggled with Ctrl+A) uses single keys for workspace
// actions. Every action is also a clickable button.
const controlKey = tcell.KeyCtrlA

type wsMode int

const (
	modeTerminal wsMode = iota
	modeControl
)

// wsNode is a node of the split layout: a session (leaf) or a split of
// several children laid out in one direction.
type wsNode struct {
	pane   *vterm.Pane
	target string // user@host or "this machine"
	dir    int    // tview.FlexColumn (side by side) or tview.FlexRow (stacked)
	kids   []*wsNode
	parent *wsNode
	server *domain.Server // nil for a local shell
	weight int            // share of the parent split (dragging a border changes it)
	prim   tview.Primitive
}

type Workspace struct {
	t *App

	root    *wsNode
	focused *wsNode
	tabs    bool // tabs layout: only the focused session is shown
	mode    wsMode
	sidebar bool // session list visible

	view     *tview.Flex
	actions  *tview.TextView
	cmdBar   *tview.TextView
	list     *tview.List
	panes    *paneArea
	body     *tview.Flex
	status   *tview.TextView
	commands []store.Command
	syncing  bool // list is being refilled; ignore its change events

	// What each sidebar row does: hover/arrow keys only switch sessions
	// (onChange); a click or Enter runs onPick.
	listRows []listRow
	empty    *tview.TextView // shown when no session is open

	zoomed   *wsNode // the session shown full screen, if any
	zoomView *tview.Flex
	zoomBar  *tview.TextView

	dialogButton func(id string) // buttons of the open dialog (ids m…)

	floats     []*floatWin // popup windows, bottom to top
	floatFocus *floatWin   // the popup being typed into, nil for a tile
	layer      *floatLayer
}

type listRow struct {
	onChange func()
	onPick   func()
}

// dirFloat as an open direction means "in a popup window".
const dirFloat = -2

func newWorkspace(t *App) *Workspace {
	w := &Workspace{t: t, sidebar: true}
	w.actions = w.buttonBar()
	w.cmdBar = w.buttonBar()
	w.list = tview.NewList().ShowSecondaryText(true).SetSecondaryTextColor(tcell.Color243).
		SetHighlightFullLine(true).SetSelectedBackgroundColor(tcell.Color238).SetSelectedTextColor(tcell.Color255)
	w.list.SetBorder(true).SetTitle(" Sessions ").SetBorderColor(tcell.Color238)
	w.list.SetChangedFunc(func(i int, _, _ string, _ rune) {
		if !w.syncing && i < len(w.listRows) && w.listRows[i].onChange != nil {
			w.listRows[i].onChange()
		}
	})
	// tview turns two quick clicks into a double-click even on different
	// rows. A second click on another row is a click; on the same row it
	// is ignored, so one server is not opened twice.
	lastClickY := -1
	w.list.SetMouseCapture(func(a tview.MouseAction, ev *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		_, y := ev.Position()
		switch a {
		case tview.MouseLeftClick:
			lastClickY = y
		case tview.MouseLeftDoubleClick:
			if y == lastClickY {
				return tview.MouseConsumed, nil
			}
			lastClickY = y
			a = tview.MouseLeftClick
		}
		return a, ev
	})
	w.list.SetSelectedFunc(func(i int, _, _ string, _ rune) {
		if i < len(w.listRows) && w.listRows[i].onPick != nil {
			w.listRows[i].onPick()
		}
	})
	w.empty = w.buttonBar()
	w.empty.SetTextAlign(tview.AlignCenter).SetWrap(true)
	w.empty.SetBackgroundColor(tcell.ColorDefault)
	w.empty.SetText("\n\n\n[::b]No session open[::-]\n\n" +
		btn("local", "▶ Open a local terminal", "#55D7FF") + "\n\n" +
		btn("new", "+ Connect to a server", "#87AFD7") + "\n\n" +
		btn("addserver", "+ Add a server", "#87D7AF") + "\n\n" +
		"[#8a8a8a]Split panes, popups and saved commands work with local terminals too.[-]")
	w.panes = newPaneArea(w)
	w.body = tview.NewFlex()
	w.status = w.buttonBar()
	w.zoomBar = w.buttonBar()
	w.zoomView = tview.NewFlex().SetDirection(tview.FlexRow)
	w.layer = newFloatLayer(w, w.body)
	w.view = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(w.actions, 1, 0, false).
		AddItem(w.cmdBar, 1, 0, false).
		AddItem(w.layer, 0, 1, true).
		AddItem(w.status, 1, 0, false)
	w.reloadCommands()
	w.refresh()
	return w
}

// buttonBar is a one-line bar of clickable regions.
func (w *Workspace) buttonBar() *tview.TextView {
	bar := tview.NewTextView().SetDynamicColors(true).SetRegions(true).SetWrap(false)
	bar.SetBackgroundColor(tcell.Color235)
	// tview reports two quick clicks (even on different buttons) as a
	// double-click; on a button bar each one is a click.
	bar.SetMouseCapture(func(a tview.MouseAction, ev *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if a == tview.MouseLeftDoubleClick {
			a = tview.MouseLeftClick
		}
		return a, ev
	})
	bar.SetHighlightedFunc(func(added, _, _ []string) {
		if len(added) == 0 {
			return
		}
		id := added[0]
		// Run after tview has finished handling the click.
		go w.t.app.QueueUpdateDraw(func() {
			bar.Highlight()
			w.button(id)
		})
	})
	return bar
}

func btn(id, label, bg string) string {
	return fmt.Sprintf(`["%s"][black:%s] %s [-:-][""] `, id, bg, label)
}

func (w *Workspace) drawActions() {
	layout := "▭ Tabs view"
	if w.tabs {
		layout = "▦ Panes view"
	}
	side := "◧ Hide list"
	if !w.sidebar {
		side = "◧ Show list"
	}
	w.actions.SetText(" " +
		btn("new", "+ New", "#55D7FF") +
		btn("splitv", "⇆ Split right", "#87AFD7") +
		btn("splith", "⇅ Split down", "#87AFD7") +
		btn("popup", "❐ Popup", "#D7AFFF") +
		btn("even", "⊟ Even sizes", "#87AFD7") +
		btn("layout", layout, "#87AFD7") +
		btn("close", "✕ Close", "#FF8787") +
		btn("side", side, "#8A8A8A") +
		btn("help", "? Help", "#8A8A8A") +
		btn("servers", "☰ Server list", "#8A8A8A"))
}

// ---------------------------------------------------------------------------
// Layout tree
// ---------------------------------------------------------------------------

func (w *Workspace) PaneCount() int { return len(w.leaves()) }

func (w *Workspace) leaves() []*wsNode {
	var out []*wsNode
	var walk func(*wsNode)
	walk = func(n *wsNode) {
		if n == nil {
			return
		}
		if n.pane != nil {
			out = append(out, n)
			return
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(w.root)
	return out
}

func (w *Workspace) insert(leaf *wsNode, dir int) {
	cur := w.focused
	switch {
	case cur == nil:
		leaf.weight = 1000
		w.root = leaf
	case cur.parent != nil && cur.parent.dir == dir:
		// The new session takes half of the current one's space.
		cur.weight = max(1, cur.weight/2)
		leaf.weight = cur.weight
		par := cur.parent
		i := indexOf(par.kids, cur)
		par.kids = append(par.kids[:i+1], append([]*wsNode{leaf}, par.kids[i+1:]...)...)
		leaf.parent = par
	default:
		split := &wsNode{dir: dir, weight: cur.weight}
		cur.weight, leaf.weight = 1000, 1000
		w.replace(cur, split)
		split.kids = []*wsNode{cur, leaf}
		cur.parent, leaf.parent = split, split
	}
	w.focused = leaf
}

// replace puts n in old's place in the tree.
func (w *Workspace) replace(old, n *wsNode) {
	n.parent = old.parent
	if old.parent == nil {
		w.root = n
		return
	}
	old.parent.kids[indexOf(old.parent.kids, old)] = n
}

func indexOf(kids []*wsNode, n *wsNode) int {
	for i, k := range kids {
		if k == n {
			return i
		}
	}
	return -1
}

func (w *Workspace) build(n *wsNode) tview.Primitive {
	if n.pane != nil {
		n.prim = n.pane
		return n.pane
	}
	f := tview.NewFlex().SetDirection(n.dir)
	for _, k := range n.kids {
		f.AddItem(w.build(k), 0, max(1, k.weight), false)
	}
	n.prim = f
	return f
}

// refresh redraws the layout, sidebar, bars and focus after any change.
func (w *Workspace) refresh() {
	w.panes.Clear()
	switch {
	case w.root == nil && len(w.floats) == 0:
		w.panes.AddItem(w.empty, 0, 1, false)
	case w.root == nil:
	case w.tabs && w.focused != nil:
		w.panes.AddItem(w.focused.pane, 0, 1, true)
	default:
		w.panes.AddItem(w.build(w.root), 0, 1, true)
	}
	w.body.Clear()
	if w.sidebar {
		w.body.AddItem(w.list, 26, 0, false)
	}
	w.body.AddItem(w.panes, 0, 1, true)
	w.fillList()
	w.drawActions()
	w.styleBorders()
	w.updateStatus()
	if p := w.activePane(); w.t.inWorkspace && p != nil && w.t.app.GetFocus() != p {
		w.t.app.SetFocus(p)
	}
}

// activeFloat is the popup being typed into, if any.
func (w *Workspace) activeFloat() *floatWin {
	if f := w.floatFocus; f != nil && !f.minimized && w.floatOf(f.node) != nil {
		return f
	}
	return nil
}

// activePane is the session keys and saved commands go to.
func (w *Workspace) activePane() *vterm.Pane {
	if f := w.activeFloat(); f != nil {
		return f.node.pane
	}
	if w.focused != nil {
		return w.focused.pane
	}
	return nil
}

func (w *Workspace) floatOf(n *wsNode) *floatWin {
	for _, f := range w.floats {
		if f.node == n {
			return f
		}
	}
	return nil
}

// focusFloat raises f, restores it if minimized and types into it.
func (w *Workspace) focusFloat(f *floatWin) {
	for i, g := range w.floats {
		if g == f {
			w.floats = append(append(w.floats[:i:i], w.floats[i+1:]...), f)
			break
		}
	}
	f.minimized = false
	w.floatFocus = f
	w.mode = modeTerminal
	w.refresh()
	w.t.app.SetFocus(f.node.pane)
}

func (w *Workspace) floatButton(f *floatWin, id string) {
	switch id {
	case "fmin":
		f.minimized = true
		w.floatFocus = nil
		w.refresh()
		w.flash("Minimized "+f.node.pane.Label()+" — click it in the bottom bar to bring it back", "#D7D7AF")
	case "fmax":
		f.maximized = !f.maximized
		w.focusFloat(f)
	case "fdock":
		w.dock(f)
	case "fzoom":
		w.zoom(f.node)
	case "frename":
		w.renameForm(f.node)
	case "fclose":
		w.closeFloat(f)
	}
}

func (w *Workspace) removeFloat(f *floatWin) {
	for i, g := range w.floats {
		if g == f {
			w.floats = append(w.floats[:i], w.floats[i+1:]...)
			break
		}
	}
	if w.floatFocus == f {
		w.floatFocus = nil
		// Type into the popup below it, if one is showing.
		for i := len(w.floats) - 1; i >= 0; i-- {
			if !w.floats[i].minimized {
				w.floatFocus = w.floats[i]
				break
			}
		}
	}
}

func (w *Workspace) closeFloat(f *floatWin) {
	f.node.pane.Close()
	w.removeFloat(f)
	w.mode = modeTerminal
	w.refresh()
}

// dock moves a popup into the tiled layout.
func (w *Workspace) dock(f *floatWin) {
	w.removeFloat(f)
	w.floatFocus = nil
	n := f.node
	n.parent, n.kids = nil, nil
	w.insert(n, w.autoDir())
	w.focus(n)
}

// popOut moves a tiled session into a popup window.
func (w *Workspace) popOut(n *wsNode) {
	w.detach(n)
	n.parent = nil
	w.addFloat(n)
}

// addFloat shows n in a new popup, cascaded over the existing ones.
func (w *Workspace) addFloat(n *wsNode) {
	_, _, lw, lh := w.layer.GetRect()
	if lw <= 0 || lh <= 0 {
		lw, lh = 120, 30
	}
	fw, fh := max(minFloatW, lw*3/5), max(minFloatH, lh*3/5)
	k := len(w.floats) % 5
	f := &floatWin{node: n, w: fw, h: fh, x: (lw-fw)/2 + 3*k, y: (lh-fh)/2 + k}
	w.floats = append(w.floats, f)
	w.focusFloat(f)
}

func (w *Workspace) fillList() {
	w.syncing = true
	defer func() { w.syncing = false }()
	w.list.Clear()
	w.listRows = w.listRows[:0]
	add := func(main, sec string, row listRow) {
		w.list.AddItem(main, sec, 0, nil)
		w.listRows = append(w.listRows, row)
	}
	cur := 0
	for i, n := range w.leaves() {
		dot := "[#5FD75F]●[-]"
		if n.pane.IsExited() {
			dot = "[#6C6C6C]○[-]"
		}
		focus := func() { w.focus(n) }
		if n == w.focused && w.activeFloat() == nil {
			cur = len(w.listRows)
		}
		add(fmt.Sprintf("%s %d %s", dot, i+1, tview.Escape(n.pane.Label())), "    "+tview.Escape(n.target), listRow{focus, focus})
	}
	for _, f := range w.floats {
		dot := "[#D7AFFF]❐[-]"
		if f.node.pane.IsExited() {
			dot = "[#6C6C6C]○[-]"
		}
		sec := "    popup · " + tview.Escape(f.node.target)
		if f.minimized {
			sec = "    minimized · " + tview.Escape(f.node.target)
		}
		if f == w.activeFloat() {
			cur = len(w.listRows)
		}
		focus := func() { w.focusFloat(f) }
		add(fmt.Sprintf("%s %s", dot, tview.Escape(f.node.pane.Label())), sec, listRow{focus, focus})
	}
	add("[#55D7FF]▶ Local terminal[-]", "", listRow{onPick: func() { w.open(nil, -1) }})

	// The saved servers: one click opens a session next to the current one.
	add("[#8a8a8a::b]─── Servers ───[-:-:-]", "    [#8a8a8a]☰ manage all[-]", listRow{onPick: w.hide})
	servers := w.t.store.List()
	sortServers(servers, SortByLastSeenDesc)
	for _, s := range servers {
		add("[#87AFD7]⇢[-] "+tview.Escape(s.Alias), "    "+tview.Escape(s.Target()), listRow{onPick: func() { w.open(&s, -1) }})
	}
	add("[#87D7AF]+ Add server[-]", "", listRow{onPick: w.addServer})
	w.list.SetCurrentItem(cur)
}

// addServer opens the server form and comes back here when it closes.
func (w *Workspace) addServer() {
	w.mode = modeTerminal
	w.t.inWorkspace = false
	w.t.backToWorkspace = true
	w.t.showServerForm(nil)
}

func (w *Workspace) focus(n *wsNode) {
	w.focused = n
	w.floatFocus = nil
	w.mode = modeTerminal
	w.refresh()
	w.t.app.SetFocus(n.pane)
}

func (w *Workspace) styleBorders() {
	active := w.activeFloat()
	for _, f := range w.floats {
		if f == active {
			f.node.pane.SetBorderColor(tcell.Color81).SetTitleColor(tcell.Color255)
		} else {
			f.node.pane.SetBorderColor(tcell.Color243).SetTitleColor(tcell.Color250)
		}
	}
	for _, n := range w.leaves() {
		switch {
		case n != w.focused || active != nil:
			n.pane.SetBorderColor(tcell.Color238).SetTitleColor(tcell.Color245)
		case w.mode == modeControl:
			n.pane.SetBorderColor(tcell.Color221).SetTitleColor(tcell.Color221)
		default:
			n.pane.SetBorderColor(tcell.Color81).SetTitleColor(tcell.Color255)
		}
	}
}

func (w *Workspace) updateStatus() {
	if w.mode == modeControl {
		w.status.SetText(" [black:#FFD75F:b] CONTROL [-:-:-] [white]v[-] split right  [white]s[-] split down  [white]n[-] new  [white]x[-] close  [white]z[-] full screen  [white]r[-] rename  [white]e[-] even sizes  [white]t[-] tabs/panes  [white]←↑↓→[-] move  [white]c[-] commands  [white]?[-] help  [white]q[-] server list  [#8a8a8a]Esc = back to typing[-]")
		return
	}
	n := w.PaneCount() + len(w.floats)
	hint := "click a pane to type in it · [white]⇆ ⇅[-] on a pane splits it · [white]❐[-] pops it out · drag a border to resize · wheel scrolls · drag copies · right-click pastes"
	if len(w.floats) > 0 {
		hint = "popups: drag the title to move · drag [white]◢[-] to resize · [white]▁[-] minimize · [white]□[-] maximize · [white]⊞[-] dock into the grid"
	}
	w.status.SetText(fmt.Sprintf(" %s[#8a8a8a]%d session%s · %s[-]", w.dockButtons(), n, plural(n), hint))
}

// dockButtons are the minimized popups, shown as buttons in the bottom bar.
func (w *Workspace) dockButtons() string {
	var b strings.Builder
	for i, f := range w.floats {
		if f.minimized {
			b.WriteString(btn(fmt.Sprintf("float%d", i), "❐ "+tview.Escape(f.node.pane.Label()), "#D7AFFF"))
		}
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// show puts the workspace on screen.
func (w *Workspace) show() {
	w.t.inWorkspace = true
	if w.zoomed != nil {
		w.refresh()
		w.showZoom()
		return
	}
	w.t.app.SetRoot(w.view, true)
	w.refresh()
}

// hide goes back to the server list; sessions keep running.
func (w *Workspace) hide() {
	w.t.inWorkspace = false
	w.mode = modeTerminal
	w.t.returnToMain()
	w.t.refreshServerList()
}

// restore puts the workspace back on screen after a picker or form.
func (w *Workspace) restore() {
	if w.zoomed != nil && w.isLive(w.zoomed) {
		w.refresh()
		w.showZoom()
		return
	}
	w.zoomed = nil
	w.t.app.SetRoot(w.view, true)
	w.refresh()
	if p := w.activePane(); p != nil {
		w.t.app.SetFocus(p)
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// autoDir splits along the focused pane's longer side.
func (w *Workspace) autoDir() int {
	if w.focused == nil {
		return tview.FlexColumn
	}
	_, _, wd, h := w.focused.pane.GetRect()
	if wd > 2*h+10 {
		return tview.FlexColumn
	}
	return tview.FlexRow
}

// open starts a session next to the focused one (or as the first one).
// s == nil opens a local shell. dir < 0 picks a direction automatically.
func (w *Workspace) open(s *domain.Server, dir int) {
	if dir < 0 && dir != dirFloat {
		dir = w.autoDir()
	}
	label, target := "local", "this machine"
	if s != nil {
		label, target = s.Alias, s.Target()
	}
	label = w.uniqueLabel(label)
	app := w.t.app
	p := vterm.NewPane(label, func() { go app.QueueUpdateDraw(func() {}) })
	p.OnCopy = func(text string) {
		copyToClipboard(text)
		w.flash(fmt.Sprintf("Copied %d characters", len([]rune(text))), "#A0FFA0")
	}
	p.OnPasteRequest = func() string {
		text, _ := clipboard.ReadAll()
		return text
	}
	p.OnExit = func() { go app.QueueUpdateDraw(w.fillList) }
	p.SetFocusFunc(func() {
		// A click on a pane (or any focus change) means "type here".
		for _, n := range w.leaves() {
			if n.pane == p && (w.focused != n || w.floatFocus != nil) {
				w.focused = n
				w.floatFocus = nil
				w.mode = modeTerminal
				w.fillList()
				w.styleBorders()
				w.updateStatus()
			}
		}
		for _, f := range w.floats {
			if f.node.pane == p && w.floatFocus != f {
				w.focusFloat(f)
			}
		}
	})
	p.SetTitleAlign(tview.AlignLeft)
	leaf := &wsNode{pane: p, target: target, server: s}
	w.mode = modeTerminal
	if dir == dirFloat {
		w.addFloat(leaf)
	} else {
		w.floatFocus = nil
		w.insert(leaf, dir)
		w.refresh()
	}

	if s == nil {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		if err := vterm.StartCommand(p, exec.Command(shell)); err != nil {
			p.Message("\x1b[38;5;203m✗ " + err.Error() + "\x1b[0m\r\n")
			p.Exited()
		}
		return
	}
	go w.connect(p, *s)
}

// uniqueLabel numbers repeated names: local, local 2, local 3…
func (w *Workspace) uniqueLabel(label string) string {
	used := map[string]bool{}
	for _, n := range w.leaves() {
		used[n.pane.Label()] = true
	}
	for _, f := range w.floats {
		used[f.node.pane.Label()] = true
	}
	name := label
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s %d", label, i)
	}
	return name
}

// closeFocused ends the session being typed into and removes its pane.
func (w *Workspace) closeFocused() {
	if w.zoomed != nil {
		w.zoomed = nil
		w.t.app.SetRoot(w.view, true)
	}
	if f := w.activeFloat(); f != nil {
		w.closeFloat(f)
		return
	}
	n := w.focused
	if n == nil {
		return
	}
	n.pane.Close()
	w.detach(n)
	w.mode = modeTerminal
	w.refresh()
	if w.activePane() == nil {
		w.t.app.SetFocus(w.empty)
	}
}

// detach takes n out of the tiled layout and focuses a neighbour.
func (w *Workspace) detach(n *wsNode) {
	leaves := w.leaves()
	var next *wsNode
	if i := indexOf(leaves, n); len(leaves) > 1 {
		if i > 0 {
			next = leaves[i-1]
		} else {
			next = leaves[1]
		}
	}
	if par := n.parent; par == nil {
		w.root = nil
	} else {
		i := indexOf(par.kids, n)
		par.kids = append(par.kids[:i], par.kids[i+1:]...)
		if len(par.kids) == 1 {
			par.kids[0].weight = par.weight
			w.replace(par, par.kids[0])
		}
	}
	if w.focused == n {
		w.focused = next
	}
}

// moveFocus focuses the nearest pane in direction (dx, dy); in tabs view
// it moves through the session list instead.
func (w *Workspace) moveFocus(dx, dy int) {
	if w.focused == nil {
		return
	}
	if w.tabs {
		w.cycleFocus(dx + dy)
		return
	}
	cx, cy := center(w.focused.pane)
	var best *wsNode
	bestDist := 1 << 30
	for _, n := range w.leaves() {
		if n == w.focused {
			continue
		}
		x, y := center(n.pane)
		ddx, ddy := x-cx, y-cy
		if (dx != 0 && ddx*dx <= 0) || (dy != 0 && ddy*dy <= 0) {
			continue
		}
		// Prefer panes straight ahead over diagonal ones.
		d := abs(ddx)*abs(dx) + abs(ddy)*abs(dy) + 3*(abs(ddx)*abs(dy)+abs(ddy)*abs(dx))
		if d < bestDist {
			best, bestDist = n, d
		}
	}
	if best != nil {
		w.focused = best
		w.floatFocus = nil
		w.refresh()
	}
}

func (w *Workspace) cycleFocus(step int) {
	leaves := w.leaves()
	if len(leaves) < 2 || step == 0 {
		return
	}
	i := indexOf(leaves, w.focused)
	w.focused = leaves[((i+step)%len(leaves)+len(leaves))%len(leaves)]
	w.floatFocus = nil
	w.refresh()
}

func center(p *vterm.Pane) (int, int) {
	x, y, wd, h := p.GetRect()
	return 2*x + wd, 2*y + h // doubled to stay in integers
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// connect logs in to s inside p. Servers lazyssh can log in to on its own
// (saved password, managed or user key) use the built-in client; anything
// else runs the system ssh in the pane so it can prompt.
func (w *Workspace) connect(p *vterm.Pane, s domain.Server) {
	p.Message(fmt.Sprintf("\x1b[38;5;81m›\x1b[0m Connecting to %s…\r\n", s.Target()))
	c := *w.t.connector
	c.Out = p.Output()
	c.PromptPassword = nil
	touch := func() {
		_, _ = w.t.store.Mutate(s.ID, func(x *domain.Server) {
			x.LastSeen = time.Now()
			x.SSHCount++
		})
	}
	if s.Password != "" || s.KeyPath != "" || s.KeyState == domain.KeyInstalled {
		client, err := c.DialAuto(s)
		if err == nil {
			if err = vterm.StartSSH(p, client); err == nil {
				touch()
				return
			}
		}
		p.Message(fmt.Sprintf("\x1b[38;5;214m!\x1b[0m %v\r\n", err))
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		p.Message("\x1b[38;5;203m✗ no ssh client installed\x1b[0m\r\n")
		p.Exited()
		return
	}
	key := s.KeyPath
	if key == "" && s.KeyState == domain.KeyInstalled {
		key = c.Key.Path
	}
	if err := vterm.StartCommand(p, exec.Command("ssh", sshx.SSHArgs(s, key)...)); err != nil {
		p.Message("\x1b[38;5;203m✗ " + err.Error() + "\x1b[0m\r\n")
		p.Exited()
		return
	}
	touch()
}

// ---------------------------------------------------------------------------
// Actions (buttons, control keys and the action finder share these)
// ---------------------------------------------------------------------------

func (w *Workspace) button(id string) {
	switch {
	case id == "new":
		w.newSessionPicker(-1)
	case id == "splitv":
		w.newSessionPicker(tview.FlexColumn)
	case id == "splith":
		w.newSessionPicker(tview.FlexRow)
	case id == "local":
		w.open(nil, -1)
	case id == "addserver":
		w.addServer()
	case strings.HasPrefix(id, "m") && w.dialogButton != nil:
		w.dialogButton(id)
	case id == "unzoom":
		w.unzoom()
	case id == "rename":
		w.renameForm(w.activeNode())
	case id == "even":
		w.evenSizes()
	case id == "popup":
		w.newSessionPicker(dirFloat)
	case strings.HasPrefix(id, "float"):
		if i, err := strconv.Atoi(strings.TrimPrefix(id, "float")); err == nil && i < len(w.floats) {
			w.focusFloat(w.floats[i])
		}
	case id == "layout":
		w.tabs = !w.tabs
		w.refresh()
	case id == "close":
		w.closeFocused()
	case id == "side":
		w.sidebar = !w.sidebar
		w.refresh()
	case id == "help":
		w.showHelp()
	case id == "servers":
		w.hide()
	case id == "save":
		w.saveCommandForm()
	case id == "cmds":
		w.pickCommand()
	case strings.HasPrefix(id, "cmd"):
		if i, err := strconv.Atoi(strings.TrimPrefix(id, "cmd")); err == nil && i < len(w.commands) {
			w.runCommand(w.commands[i])
		}
	}
}

// paneButton runs a button from a pane's title bar on that pane.
func (w *Workspace) paneButton(n *wsNode, id string) {
	if indexOf(w.leaves(), n) < 0 {
		return
	}
	w.focused = n
	w.mode = modeTerminal
	switch id {
	case "splitv":
		w.newSessionPicker(tview.FlexColumn)
	case "splith":
		w.newSessionPicker(tview.FlexRow)
	case "zoom":
		w.zoom(n)
	case "rename":
		w.renameForm(n)
	case "float":
		w.popOut(n)
	case "close":
		w.closeFocused()
	}
}

// isShortcut reports Ctrl+Shift+<r>, however the terminal encodes it.
func isShortcut(ev *tcell.EventKey, r rune) bool {
	m := ev.Modifiers()
	if m&tcell.ModCtrl == 0 || m&tcell.ModShift == 0 {
		return false
	}
	if ev.Key() == tcell.KeyRune {
		return ev.Rune() == r || ev.Rune() == r-'a'+'A'
	}
	return ev.Key() == tcell.KeyCtrlA+tcell.Key(r-'a')
}

// handleKey is the application-level key handler while the workspace is
// on screen. It returns nil for keys it consumed.
func (w *Workspace) handleKey(ev *tcell.EventKey) *tcell.EventKey {
	p, onPane := w.t.app.GetFocus().(*vterm.Pane)
	if w.t.app.GetFocus() == w.empty && ev.Key() == tcell.KeyEnter {
		w.open(nil, -1)
		return nil
	}
	if !onPane {
		return ev // a picker or form is open
	}
	if w.mode == modeControl {
		w.controlKey(ev, p)
		w.styleBorders()
		w.updateStatus()
		return nil
	}
	alt := ev.Modifiers()&tcell.ModAlt != 0
	// Ctrl+Shift+C / Ctrl+Shift+V copy and paste like other terminals
	// (when the terminal sends them as such and not as Ctrl+C).
	if isShortcut(ev, 'c') {
		if !p.CopySelection() {
			w.flash("Nothing selected — drag over text to select it (it is copied right away)", "#FFD75F")
		}
		return nil
	}
	if isShortcut(ev, 'v') {
		if text, _ := clipboard.ReadAll(); text != "" {
			p.Paste(text)
		}
		return nil
	}
	switch {
	case ev.Key() == controlKey:
		w.mode = modeControl
		w.styleBorders()
		w.updateStatus()
	case alt && ev.Key() == tcell.KeyLeft:
		w.moveFocus(-1, 0)
	case alt && ev.Key() == tcell.KeyRight:
		w.moveFocus(1, 0)
	case alt && ev.Key() == tcell.KeyUp:
		w.moveFocus(0, -1)
	case alt && ev.Key() == tcell.KeyDown:
		w.moveFocus(0, 1)
	case p.IsExited() && ev.Key() == tcell.KeyEnter:
		w.closeFocused()
	case ev.Key() == tcell.KeyCtrlC:
		// tview would quit the whole app on Ctrl+C; it belongs to the pane.
		p.Type(string([]byte{vterm.CtrlByte(tcell.KeyCtrlC)}))
	default:
		return ev
	}
	return nil
}

func (w *Workspace) controlKey(ev *tcell.EventKey, p *vterm.Pane) {
	back := func() { w.mode = modeTerminal }
	switch ev.Key() {
	case controlKey: // pressed twice: send a real Ctrl+A
		p.Type(string([]byte{vterm.CtrlByte(controlKey)}))
		back()
		return
	case tcell.KeyEsc, tcell.KeyEnter:
		back()
		return
	case tcell.KeyLeft:
		w.moveFocus(-1, 0)
		return
	case tcell.KeyRight:
		w.moveFocus(1, 0)
		return
	case tcell.KeyUp:
		w.moveFocus(0, -1)
		return
	case tcell.KeyDown:
		w.moveFocus(0, 1)
		return
	case tcell.KeyTab:
		w.cycleFocus(1)
		return
	}
	switch r := ev.Rune(); r {
	case 'v', '|':
		w.newSessionPicker(tview.FlexColumn)
	case 's', '-':
		w.newSessionPicker(tview.FlexRow)
	case 'n':
		w.newSessionPicker(-1)
	case 'x':
		w.closeFocused()
	case 't', 'L':
		w.tabs = !w.tabs
		w.refresh()
	case 'z':
		w.toggleZoom(w.activeNode())
	case 'r':
		w.renameForm(w.activeNode())
	case 'e':
		w.evenSizes()
	case 'h':
		w.moveFocus(-1, 0)
	case 'l':
		w.moveFocus(1, 0)
	case 'k':
		w.moveFocus(0, -1)
	case 'j':
		w.moveFocus(0, 1)
	case 'p', ']':
		w.cycleFocus(1)
	case '[':
		w.cycleFocus(-1)
	case 'c':
		w.pickCommand()
	case 'a':
		w.saveCommandForm()
	case 'b':
		w.sidebar = !w.sidebar
		w.refresh()
	case '?':
		w.showHelp()
	case 'q':
		w.hide()
	case 'i':
		back()
	default:
		if r >= '1' && r <= '9' {
			if i := int(r - '1'); i < len(w.commands) {
				w.runCommand(w.commands[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Saved commands
// ---------------------------------------------------------------------------

func (w *Workspace) reloadCommands() {
	if cmds, err := w.t.store.Commands(); err == nil {
		w.commands = cmds
	}
	var b strings.Builder
	b.WriteString(" ")
	b.WriteString(btn("cmds", "★ Commands", "#FFD75F"))
	b.WriteString(btn("save", "+ Add", "#D7D7AF"))
	for i, c := range w.commands {
		num := ""
		if i < 9 {
			num = strconv.Itoa(i+1) + " "
		}
		fmt.Fprintf(&b, `["cmd%d"][#d0d0d0:#3a3a3a] [#FFD75F]%s[#d0d0d0]%s [-:-][""] `, i, num, tview.Escape(truncate(c.Label(), 24)))
	}
	if len(w.commands) == 0 {
		b.WriteString("[#8a8a8a]keep commands you use often: click + Add, then one click types them into a session[-]")
	}
	w.cmdBar.SetText(b.String())
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// runCommand types c into the focused session without pressing Enter, so
// it can be checked or edited first.
func (w *Workspace) runCommand(c store.Command) {
	w.mode = modeTerminal
	w.restore()
	if p := w.activePane(); p != nil {
		p.Type(c.Cmd)
	}
	go func() { _ = w.t.store.TouchCommand(c.ID) }()
}

// promptGuess extracts the command being typed from a shell prompt line.
func promptGuess(line string) string {
	// Screen lines lose their trailing spaces: "user@host:~$ " reads as
	// "user@host:~$", an empty prompt.
	line += " "
	for _, sep := range []string{"$ ", "# ", "% ", "> ", "❯ "} {
		if i := strings.LastIndex(line, sep); i >= 0 {
			return strings.TrimSpace(line[i+len(sep):])
		}
	}
	return strings.TrimSpace(line)
}

func (w *Workspace) saveCommandForm() { w.commandForm(nil) }

// flash shows msg in the status line for a few seconds.
func (w *Workspace) flash(msg, color string) {
	w.status.SetText(" " + w.dockButtons() + "[" + color + "]" + tview.Escape(msg) + "[-]")
	time.AfterFunc(2*time.Second, func() { w.t.app.QueueUpdateDraw(w.updateStatus) })
}

func (w *Workspace) pickCommand() { w.commandManager() }

// newSessionPicker asks which server to open. dir < 0 = automatic.
func (w *Workspace) newSessionPicker(dir int) {
	w.mode = modeTerminal
	servers := w.t.store.List()
	sortServers(servers, SortByLastSeenDesc)
	var items []pickItem
	var opens []func()
	add := func(main, sec, search string, open func()) {
		items = append(items, pickItem{main: main, secondary: sec, search: search})
		opens = append(opens, open)
	}
	// Splitting a server session: opening the same server again is the
	// usual wish (logs on one side, a shell on the other).
	if cur := w.focused; cur != nil && cur.server != nil {
		same := *cur.server
		add("Same server — "+same.Alias, same.Target(), "same "+same.Alias, func() { w.open(&same, dir) })
	}
	add("Local shell", "this machine", "local shell", func() { w.open(nil, dir) })
	for _, s := range servers {
		add(s.Alias, s.Target(), s.Alias+" "+s.Target()+" "+strings.Join(s.Tags, " "), func() { w.open(&s, dir) })
	}
	title := " New session — pick a server "
	switch dir {
	case tview.FlexColumn:
		title = " Split right — pick a server "
	case tview.FlexRow:
		title = " Split down — pick a server "
	case dirFloat:
		title = " Popup window — pick a server "
	}
	w.showPicker(title, items, func(i int) {
		w.restore()
		opens[i]()
	}, nil)
}

func (w *Workspace) showHelp() {
	w.mode = modeTerminal
	text := `[#55D7FF::b]Buttons on each session's title bar[-:-:-]
  ✎ Rename       give it a name you remember (app logs, db shell…)
  ⇆ ⇅            split it right / down        ✕  close it
  ❐ Pop out      move it into a popup window  ⊞  dock a popup back
  ⤢ Full screen  fill the whole terminal; ⤡ in the top bar goes back
                 (double-clicking a title bar does the same)
  Popups also:   ▁ minimize to the bottom bar · □ maximize · drag the
                 title to move · drag ◢ to resize

[#55D7FF::b]Top bars[-:-:-]
  ⊟ Even sizes   same space for every split pane (drag borders to resize)
  ★ Commands     your saved commands: ▶ type · ↵ run · + new · ✎ edit ·
                 ✕ remove. + Add saves one; a chip types it in one click

[#55D7FF::b]Mouse — works like a normal terminal[-:-:-]
  Click a session to type in it · wheel scrolls back · drag selects and
  copies · double-click selects a word · right-click pastes ·
  Shift + drag selects inside mouse apps (htop, vim, nvim…)
  Text too small/big? Zoom your terminal (Ctrl+Shift+= / Ctrl+-):
  every pane and program re-fits to the new size.

[#FFD75F::b]Keyboard (optional)[-:-:-]
  Ctrl+C always goes to the program. Alt+←↑↓→ moves between sessions.
  Ctrl+A, then: v/s split · n new · x close · z full screen · r rename
  e even sizes · t tabs view · c commands · a add command
  1-9 type a command · ←↑↓→ move · Esc back · Ctrl+A again sends Ctrl+A

[#8a8a8a]Esc closes this help[-]`
	tv := tview.NewTextView().SetDynamicColors(true).SetText(text)
	tv.SetBorder(true).SetTitle(" Help ")
	tv.SetInputCapture(func(*tcell.EventKey) *tcell.EventKey { w.restore(); return nil })
	tv.SetMouseCapture(func(a tview.MouseAction, ev *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if a == tview.MouseLeftClick {
			w.restore()
			return a, nil
		}
		return a, ev
	})
	w.overlay(tv, 80, 36, tv)
}

type pickItem struct{ main, secondary, search string }

// showPicker is a filterable list. onDelete may be nil.
func (w *Workspace) showPicker(title string, items []pickItem, onPick, onDelete func(int)) {
	input := tview.NewInputField().SetLabel(" 🔍 ").SetFieldBackgroundColor(tcell.Color235).
		SetPlaceholder("type to filter · ↑↓ choose · Enter open · Esc cancel").SetPlaceholderTextColor(tcell.Color242)
	list := tview.NewList().ShowSecondaryText(true).SetSecondaryTextColor(tcell.Color245)
	list.SetHighlightFullLine(true).SetSelectedBackgroundColor(tcell.Color238)
	var shown []int
	fill := func(q string) {
		list.Clear()
		shown = shown[:0]
		for i, it := range items {
			if q != "" {
				if _, ok := search.Score(q, it.search); !ok {
					continue
				}
			}
			shown = append(shown, i)
			list.AddItem(tview.Escape(it.main), tview.Escape(it.secondary), 0, nil)
		}
	}
	fill("")
	pick := func() {
		if i := list.GetCurrentItem(); i >= 0 && i < len(shown) {
			onPick(shown[i])
		}
	}
	input.SetChangedFunc(fill)
	input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEsc:
			w.restore()
		case tcell.KeyEnter:
			pick()
		case tcell.KeyDown:
			list.SetCurrentItem((list.GetCurrentItem() + 1) % max(1, list.GetItemCount()))
		case tcell.KeyUp:
			list.SetCurrentItem(max(0, list.GetCurrentItem()-1))
		case tcell.KeyCtrlD:
			if i := list.GetCurrentItem(); onDelete != nil && i >= 0 && i < len(shown) {
				onDelete(shown[i])
			}
		default:
			return ev
		}
		return nil
	})
	// A click picks the item (no Enter needed).
	list.SetSelectedFunc(func(int, string, string, rune) { pick() })
	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEsc {
			w.restore()
			return nil
		}
		return ev
	})
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(input, 1, 0, true).AddItem(list, 0, 1, false)
	box.SetBorder(true).SetTitle(title)
	w.overlay(box, 80, 22, input)
}

func centered(p tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p, height, 0, true).
			AddItem(nil, 0, 1, false), width, 0, true).
		AddItem(nil, 0, 1, false)
}
