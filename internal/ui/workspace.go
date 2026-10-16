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
	panes    *tview.Flex
	body     *tview.Flex
	status   *tview.TextView
	commands []store.Command
	syncing  bool // list is being refilled; ignore its change events
}

func newWorkspace(t *App) *Workspace {
	w := &Workspace{t: t, sidebar: true}
	w.actions = w.buttonBar()
	w.cmdBar = w.buttonBar()
	w.list = tview.NewList().ShowSecondaryText(true).SetSecondaryTextColor(tcell.Color243).
		SetHighlightFullLine(true).SetSelectedBackgroundColor(tcell.Color238).SetSelectedTextColor(tcell.Color255)
	w.list.SetBorder(true).SetTitle(" Sessions ").SetBorderColor(tcell.Color238)
	w.list.SetChangedFunc(func(i int, _, _ string, _ rune) {
		if !w.syncing {
			w.listPick(i)
		}
	})
	w.list.SetSelectedFunc(func(i int, _, _ string, _ rune) { w.listPick(i) })
	w.panes = tview.NewFlex()
	w.body = tview.NewFlex()
	w.status = tview.NewTextView().SetDynamicColors(true)
	w.status.SetBackgroundColor(tcell.Color235)
	w.view = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(w.actions, 1, 0, false).
		AddItem(w.cmdBar, 1, 0, false).
		AddItem(w.body, 0, 1, true).
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
		btn("new", "+ New session", "#55D7FF") +
		btn("splitv", "⇆ Split right", "#87AFD7") +
		btn("splith", "⇅ Split down", "#87AFD7") +
		btn("layout", layout, "#87AFD7") +
		btn("close", "✕ Close", "#FF8787") +
		btn("palette", "⌕ Find action", "#D7D7AF") +
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
		w.root = leaf
	case cur.parent != nil && cur.parent.dir == dir:
		par := cur.parent
		i := indexOf(par.kids, cur)
		par.kids = append(par.kids[:i+1], append([]*wsNode{leaf}, par.kids[i+1:]...)...)
		leaf.parent = par
	default:
		split := &wsNode{dir: dir}
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
		return n.pane
	}
	f := tview.NewFlex().SetDirection(n.dir)
	for _, k := range n.kids {
		f.AddItem(w.build(k), 0, 1, false)
	}
	return f
}

// refresh redraws the layout, sidebar, bars and focus after any change.
func (w *Workspace) refresh() {
	w.panes.Clear()
	switch {
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
	if w.t.inWorkspace && w.focused != nil && w.t.app.GetFocus() != w.focused.pane {
		w.t.app.SetFocus(w.focused.pane)
	}
}

func (w *Workspace) fillList() {
	w.syncing = true
	defer func() { w.syncing = false }()
	w.list.Clear()
	cur := 0
	for i, n := range w.leaves() {
		dot := "[#5FD75F]●[-]"
		if n.pane.IsExited() {
			dot = "[#6C6C6C]○[-]"
		}
		w.list.AddItem(fmt.Sprintf("%s %d %s", dot, i+1, tview.Escape(n.pane.Label())), "    "+tview.Escape(n.target), 0, nil)
		if n == w.focused {
			cur = i
		}
	}
	w.list.AddItem("[#55D7FF]+ New session[-]", "", 0, nil)
	w.list.SetCurrentItem(cur)
}

func (w *Workspace) listPick(i int) {
	leaves := w.leaves()
	if i >= len(leaves) {
		w.newSessionPicker(-1)
		return
	}
	w.focus(leaves[i])
}

func (w *Workspace) focus(n *wsNode) {
	w.focused = n
	w.mode = modeTerminal
	w.refresh()
	w.t.app.SetFocus(n.pane)
}

func (w *Workspace) styleBorders() {
	for _, n := range w.leaves() {
		switch {
		case n != w.focused:
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
		w.status.SetText(" [black:#FFD75F:b] CONTROL [-:-:-] [white]v[-] split right  [white]s[-] split down  [white]n[-] new  [white]x[-] close  [white]t[-] tabs/panes  [white]←↑↓→[-] move  [white]Space[-] find action  [white]c[-] commands  [white]?[-] help  [white]q[-] server list  [#8a8a8a]Esc = back to typing[-]")
		return
	}
	n := w.PaneCount()
	w.status.SetText(fmt.Sprintf(" [black:#55D7FF:b] TERMINAL [-:-:-] [#8a8a8a]%d session%s · typing goes to the highlighted pane · mouse wheel scrolls · drag to copy · right-click pastes ·[-] [white]Ctrl+A[-] [#8a8a8a]control keys[-]", n, plural(n)))
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
	w.t.app.SetRoot(w.view, true)
	w.refresh()
	if w.focused != nil {
		w.t.app.SetFocus(w.focused.pane)
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
	if dir < 0 {
		dir = w.autoDir()
	}
	label, target := "local", "this machine"
	if s != nil {
		label, target = s.Alias, s.Target()
	}
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
			if n.pane == p && w.focused != n {
				w.focused = n
				w.mode = modeTerminal
				w.fillList()
				w.styleBorders()
				w.updateStatus()
			}
		}
	})
	leaf := &wsNode{pane: p, target: target}
	w.insert(leaf, dir)
	w.mode = modeTerminal
	w.refresh()

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

// closeFocused ends the focused session and removes its pane.
func (w *Workspace) closeFocused() {
	n := w.focused
	if n == nil {
		return
	}
	n.pane.Close()
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
			w.replace(par, par.kids[0])
		}
	}
	w.focused = next
	w.mode = modeTerminal
	if w.root == nil {
		w.refresh()
		w.hide()
		return
	}
	w.refresh()
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
	case id == "layout":
		w.tabs = !w.tabs
		w.refresh()
	case id == "close":
		w.closeFocused()
	case id == "palette":
		w.actionFinder()
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

// handleKey is the application-level key handler while the workspace is
// on screen. It returns nil for keys it consumed.
func (w *Workspace) handleKey(ev *tcell.EventKey) *tcell.EventKey {
	p, onPane := w.t.app.GetFocus().(*vterm.Pane)
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
	case 't', 'z', 'L':
		w.tabs = !w.tabs
		w.refresh()
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
	case ' ':
		w.actionFinder()
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
	b.WriteString(" [#FFD75F::b]★ Commands[-:-:-] ")
	b.WriteString(btn("save", "+ Save", "#FFD75F"))
	b.WriteString(btn("cmds", "☰ All", "#D7D7AF"))
	for i, c := range w.commands {
		num := ""
		if i < 9 {
			num = strconv.Itoa(i+1) + " "
		}
		fmt.Fprintf(&b, `["cmd%d"][#d0d0d0:#3a3a3a] [#FFD75F]%s[#d0d0d0]%s [-:-][""] `, i, num, tview.Escape(truncate(c.Label(), 24)))
	}
	if len(w.commands) == 0 {
		b.WriteString("[#8a8a8a]type a command in a session, then click + Save to keep it here[-]")
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
	if w.focused == nil {
		return
	}
	w.focused.pane.Type(c.Cmd)
	go func() { _ = w.t.store.TouchCommand(c.ID) }()
}

// promptGuess extracts the command being typed from a shell prompt line.
func promptGuess(line string) string {
	for _, sep := range []string{"$ ", "# ", "% ", "> ", "❯ "} {
		if i := strings.LastIndex(line, sep); i >= 0 {
			return strings.TrimSpace(line[i+len(sep):])
		}
	}
	return strings.TrimSpace(line)
}

func (w *Workspace) saveCommandForm() {
	w.mode = modeTerminal
	guess := ""
	if w.focused != nil {
		guess = promptGuess(w.focused.pane.CursorLine())
	}
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" Save command ").SetTitleAlign(tview.AlignCenter)
	form.AddInputField("Command", guess, 70, nil, nil)
	form.AddInputField("Name (optional)", "", 30, nil, nil)
	cmdField := form.GetFormItem(0).(*tview.InputField)
	nameField := form.GetFormItem(1).(*tview.InputField)
	nameField.SetPlaceholder("short label for the bar").SetPlaceholderTextColor(tcell.Color242)
	form.AddButton("Save", func() {
		if _, err := w.t.store.AddCommand(nameField.GetText(), cmdField.GetText()); err != nil {
			w.restore()
			w.flash(err.Error(), "#FF6B6B")
			return
		}
		w.reloadCommands()
		w.restore()
	})
	form.AddButton("Cancel", w.restore)
	form.SetCancelFunc(w.restore)
	w.t.app.SetRoot(centered(form, 90, 9), true).SetFocus(form)
}

// flash shows msg in the status line for a few seconds.
func (w *Workspace) flash(msg, color string) {
	w.status.SetText(" [" + color + "]" + tview.Escape(msg) + "[-]")
	time.AfterFunc(2*time.Second, func() { w.t.app.QueueUpdateDraw(w.updateStatus) })
}

func (w *Workspace) pickCommand() {
	w.mode = modeTerminal
	w.reloadCommands()
	if len(w.commands) == 0 {
		w.flash("No saved commands yet — type one in a session and click + Save", "#FFD75F")
		return
	}
	items := make([]pickItem, len(w.commands))
	for i, c := range w.commands {
		sec := ""
		if c.Name != "" {
			sec = c.Cmd
		}
		items[i] = pickItem{main: c.Label(), secondary: sec, search: c.Name + " " + c.Cmd}
	}
	w.showPicker(" Saved commands — Enter types it in the session · Ctrl+D deletes ", items, func(i int) {
		w.runCommand(w.commands[i])
	}, func(i int) {
		_ = w.t.store.DeleteCommand(w.commands[i].ID)
		w.reloadCommands()
		w.pickCommand()
	})
}

// newSessionPicker asks which server to open. dir < 0 = automatic.
func (w *Workspace) newSessionPicker(dir int) {
	w.mode = modeTerminal
	servers := w.t.store.List()
	sortServers(servers, SortByLastSeenDesc)
	items := []pickItem{{main: "Local shell", secondary: "this machine", search: "local shell"}}
	for _, s := range servers {
		items = append(items, pickItem{main: s.Alias, secondary: s.Target(), search: s.Alias + " " + s.Target() + " " + strings.Join(s.Tags, " ")})
	}
	w.showPicker(" New session — pick a server ", items, func(i int) {
		w.restore()
		if i == 0 {
			w.open(nil, dir)
			return
		}
		s := servers[i-1]
		w.open(&s, dir)
	}, nil)
}

// actionFinder lists every action, server and saved command in one
// searchable list.
func (w *Workspace) actionFinder() {
	w.mode = modeTerminal
	type entry struct {
		item pickItem
		run  func()
	}
	var entries []entry
	add := func(main, sec string, run func()) {
		entries = append(entries, entry{pickItem{main: main, secondary: sec, search: main + " " + sec}, run})
	}
	add("New session", "pick a server, opens next to the current one", func() { w.newSessionPicker(-1) })
	add("Split right", "open a server side by side", func() { w.newSessionPicker(tview.FlexColumn) })
	add("Split down", "open a server below", func() { w.newSessionPicker(tview.FlexRow) })
	add("Switch tabs / panes view", "one session full screen, or all tiled", func() { w.tabs = !w.tabs; w.restore() })
	add("Close session", "end the highlighted session", func() { w.restore(); w.closeFocused() })
	add("Save command", "keep the command being typed in the bar", w.saveCommandForm)
	add("Show / hide session list", "", func() { w.sidebar = !w.sidebar; w.restore() })
	add("Back to server list", "sessions keep running", w.hide)
	add("Help", "mouse and keyboard cheatsheet", w.showHelp)
	for _, n := range w.leaves() {
		add("Go to "+n.pane.Label(), n.target, func() { w.restore(); w.focus(n) })
	}
	for _, s := range w.t.store.List() {
		add("Open "+s.Alias, s.Target(), func() { w.restore(); w.open(&s, -1) })
	}
	for _, c := range w.commands {
		add("Type "+c.Label(), c.Cmd, func() { w.runCommand(c) })
	}
	items := make([]pickItem, len(entries))
	for i, e := range entries {
		items[i] = e.item
	}
	w.showPicker(" Find action ", items, func(i int) { entries[i].run() }, nil)
}

func (w *Workspace) showHelp() {
	w.mode = modeTerminal
	text := `[#55D7FF::b]Mouse — works like a normal terminal[-:-:-]
  Click a session          type into it
  Wheel                    scroll back through its output
  Drag                     select text (copied automatically)
  Double-click             select a word
  Right-click              paste
  Shift + drag             select inside mouse apps (htop, vim…)
  Buttons on top           every action, no keys needed

[#55D7FF::b]Keyboard — TERMINAL mode (default)[-:-:-]
  Every key goes to the highlighted session. Ctrl+C never quits lazyssh.
  Alt+←↑↓→                 move between sessions
  Shift+PgUp / PgDn        scroll back
  Ctrl+A                   switch to CONTROL mode (border turns yellow)

[#FFD75F::b]Keyboard — CONTROL mode[-:-:-]
  v  split right     s  split down     n  new session    x  close
  t  tabs / panes    ←↑↓→ or hjkl  move     [ ]  previous / next
  Space  find action    c  commands    a  save command    1-9  type command
  b  session list    q  server list     ?  help
  Esc, Enter or i    back to TERMINAL    Ctrl+A again  send a real Ctrl+A

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
	w.t.app.SetRoot(centered(tv, 82, 30), true).SetFocus(tv)
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
	w.t.app.SetRoot(centered(box, 80, 22), true).SetFocus(input)
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
