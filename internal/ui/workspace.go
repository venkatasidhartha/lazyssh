package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
	"lazyssh/internal/search"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
	"lazyssh/internal/vterm"
)

// The workspace shows several terminal sessions side by side. Keys go to
// the focused pane, except after the prefix key (Ctrl+B, like tmux).
const prefixKey = tcell.KeyCtrlB

// wsNode is a node of the split layout: a pane (leaf) or a split of
// several children laid out in one direction.
type wsNode struct {
	pane   *vterm.Pane
	dir    int // tview.FlexColumn (side by side) or tview.FlexRow (stacked)
	kids   []*wsNode
	parent *wsNode
}

type Workspace struct {
	t *App

	root    *wsNode
	focused *wsNode
	zoomed  bool
	prefix  bool // prefix key pressed, waiting for a command key

	view     *tview.Flex // toolbar + panes + status
	toolbar  *tview.TextView
	panes    *tview.Flex
	status   *tview.TextView
	commands []store.Command
}

func newWorkspace(t *App) *Workspace {
	w := &Workspace{t: t}
	w.toolbar = tview.NewTextView().SetDynamicColors(true).SetRegions(true).SetWrap(false)
	w.toolbar.SetBackgroundColor(tcell.Color235)
	w.toolbar.SetHighlightedFunc(func(added, _, _ []string) {
		if len(added) == 0 {
			return
		}
		id := added[0]
		// Run after tview finishes handling the click.
		go t.app.QueueUpdateDraw(func() {
			w.toolbar.Highlight()
			w.toolbarClick(id)
		})
	})
	w.panes = tview.NewFlex()
	w.status = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	w.status.SetBackgroundColor(tcell.Color235)
	w.view = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(w.toolbar, 1, 0, false).
		AddItem(w.panes, 0, 1, true).
		AddItem(w.status, 1, 0, false)
	w.reloadCommands()
	w.updateStatus()
	return w
}

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

// show puts the workspace on screen.
func (w *Workspace) show() {
	w.t.inWorkspace = true
	w.rebuild()
	w.t.app.SetRoot(w.view, true)
	w.focusCurrent()
}

// hide goes back to the server list; sessions keep running.
func (w *Workspace) hide() {
	w.t.inWorkspace = false
	w.prefix = false
	w.t.returnToMain()
	w.t.refreshServerList()
}

func (w *Workspace) focusCurrent() {
	if w.focused != nil {
		w.t.app.SetFocus(w.focused.pane)
	}
	w.updateStatus()
}

// rebuild recreates the flex tree from the layout.
func (w *Workspace) rebuild() {
	w.panes.Clear()
	if w.root == nil {
		return
	}
	if w.zoomed && w.focused != nil {
		w.panes.AddItem(w.focused.pane, 0, 1, true)
		return
	}
	w.panes.AddItem(w.build(w.root), 0, 1, true)
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

// open adds a pane next to the focused one (or as the first pane) and
// starts a session in it. s == nil opens a local shell.
func (w *Workspace) open(s *domain.Server, dir int) {
	label := "local"
	if s != nil {
		label = s.Alias
	}
	p := vterm.NewPane(label, func() { go w.t.app.QueueUpdateDraw(func() {}) })
	p.SetBorderColor(tcell.Color238).SetTitleColor(tcell.Color245)
	p.SetFocusFunc(func() { p.SetBorderColor(tcell.Color81).SetTitleColor(tcell.Color252) })
	p.SetBlurFunc(func() { p.SetBorderColor(tcell.Color238).SetTitleColor(tcell.Color245) })
	leaf := &wsNode{pane: p}
	w.insert(leaf, dir)
	w.zoomed = false
	w.rebuild()
	w.focusCurrent()

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
		split := &wsNode{dir: dir, parent: cur.parent}
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

// closeFocused stops the focused pane's session and removes it.
func (w *Workspace) closeFocused() {
	n := w.focused
	if n == nil {
		return
	}
	n.pane.Close()
	leaves := w.leaves()
	next := (*wsNode)(nil)
	if i := indexOf(leaves, n); len(leaves) > 1 {
		if i > 0 {
			next = leaves[i-1]
		} else {
			next = leaves[1]
		}
	}
	par := n.parent
	if par == nil {
		w.root = nil
	} else {
		par.kids = append(par.kids[:indexOf(par.kids, n)], par.kids[indexOf(par.kids, n)+1:]...)
		if len(par.kids) == 1 {
			w.replace(par, par.kids[0])
		}
	}
	w.focused = next
	w.zoomed = false
	if w.root == nil {
		w.hide()
		return
	}
	w.rebuild()
	w.focusCurrent()
}

// moveFocus focuses the nearest pane in direction (dx, dy).
func (w *Workspace) moveFocus(dx, dy int) {
	if w.focused == nil || w.zoomed {
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
		w.focusCurrent()
	}
}

func (w *Workspace) cycleFocus(step int) {
	leaves := w.leaves()
	if len(leaves) < 2 {
		return
	}
	i := indexOf(leaves, w.focused)
	w.focused = leaves[(i+step+len(leaves))%len(leaves)]
	w.rebuild()
	w.focusCurrent()
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
// Keys
// ---------------------------------------------------------------------------

// handleKey is the application-level key handler while the workspace is on
// screen. It returns nil for keys it consumed.
func (w *Workspace) handleKey(ev *tcell.EventKey) *tcell.EventKey {
	focus := w.t.app.GetFocus()
	p, onPane := focus.(*vterm.Pane)
	if !onPane {
		return ev // a picker or form is open
	}
	if w.prefix {
		w.prefix = false
		w.prefixCommand(ev, p)
		w.updateStatus()
		return nil
	}
	switch {
	case ev.Key() == prefixKey:
		w.prefix = true
		w.updateStatus()
		return nil
	case ev.Modifiers()&tcell.ModAlt != 0 && ev.Key() == tcell.KeyLeft:
		w.moveFocus(-1, 0)
		return nil
	case ev.Modifiers()&tcell.ModAlt != 0 && ev.Key() == tcell.KeyRight:
		w.moveFocus(1, 0)
		return nil
	case ev.Modifiers()&tcell.ModAlt != 0 && ev.Key() == tcell.KeyUp:
		w.moveFocus(0, -1)
		return nil
	case ev.Modifiers()&tcell.ModAlt != 0 && ev.Key() == tcell.KeyDown:
		w.moveFocus(0, 1)
		return nil
	case p.IsExited() && ev.Key() == tcell.KeyEnter:
		w.closeFocused()
		return nil
	case ev.Key() == tcell.KeyCtrlC:
		// tview would quit the whole app on Ctrl+C; it belongs to the pane.
		p.Send([]byte{vterm.CtrlByte(tcell.KeyCtrlC)})
		return nil
	}
	return ev
}

func (w *Workspace) prefixCommand(ev *tcell.EventKey, p *vterm.Pane) {
	switch ev.Key() {
	case prefixKey:
		p.Send([]byte{vterm.CtrlByte(prefixKey)})
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
	case tcell.KeyEsc:
		return
	}
	switch r := ev.Rune(); r {
	case '|', '%', 'v':
		w.pickServer(tview.FlexColumn)
	case '-', '"', 's':
		w.pickServer(tview.FlexRow)
	case 'h':
		w.moveFocus(-1, 0)
	case 'l':
		w.moveFocus(1, 0)
	case 'k':
		w.moveFocus(0, -1)
	case 'j':
		w.moveFocus(0, 1)
	case 'o':
		w.cycleFocus(1)
	case 'x':
		w.closeFocused()
	case 'z':
		if w.PaneCount() > 1 {
			w.zoomed = !w.zoomed
			w.rebuild()
			w.focusCurrent()
		}
	case 'c':
		w.pickCommand()
	case 'a':
		w.saveCommandForm()
	case 'w', 'q', 'd':
		w.hide()
	default:
		if r >= '1' && r <= '9' {
			if i := int(r - '1'); i < len(w.commands) {
				w.runCommand(w.commands[i])
			}
		}
	}
}

func (w *Workspace) updateStatus() {
	if w.prefix {
		w.status.SetText("[black:#55D7FF:b] PREFIX [-:-:-]  [white]|[-] Split right  • [white]-[-] Split down  • [white]←↑↓→/hjkl[-] Move  • [white]x[-] Close  • [white]z[-] Zoom  • [white]c[-] Commands  • [white]a[-] Save cmd  • [white]1-9[-] Run cmd  • [white]w[-] Server list")
		return
	}
	n := w.PaneCount()
	label := ""
	if w.focused != nil {
		label = w.focused.pane.Label()
	}
	zoom := ""
	if w.zoomed {
		zoom = " [black:#FFD75F] ZOOM [-:-:-]"
	}
	w.status.SetText(fmt.Sprintf("[#55D7FF::b]%s[-:-:-]%s  [#8a8a8a](%d pane%s)[-]  • [white]Ctrl+B[-] then [white]|[-]/[white]-[-] Split  • [white]Alt+←↑↓→[-] Move  • [white]Ctrl+B c[-] Commands  • [white]Ctrl+B w[-] Server list",
		tview.Escape(label), zoom, n, plural(n)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---------------------------------------------------------------------------
// Saved commands
// ---------------------------------------------------------------------------

func (w *Workspace) reloadCommands() {
	cmds, err := w.t.store.Commands()
	if err == nil {
		w.commands = cmds
	}
	var b strings.Builder
	b.WriteString(` ["save"][black:#55D7FF:b] + Save cmd [-:-:-][""] ["list"][black:#A0FFA0:b] ☰ Commands [-:-:-][""] `)
	for i, c := range w.commands {
		key := " "
		if i < 9 {
			key = strconv.Itoa(i + 1)
		}
		fmt.Fprintf(&b, ` ["cmd%d"][#FFD75F]%s[-] [#d0d0d0:#444444] %s [-:-][""]`, i, key, tview.Escape(truncate(c.Label(), 24)))
	}
	if len(w.commands) == 0 {
		b.WriteString(" [#8a8a8a]No saved commands yet — type a command in a pane, then Ctrl+B a to save it[-]")
	}
	w.toolbar.SetText(b.String())
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (w *Workspace) toolbarClick(id string) {
	switch {
	case id == "save":
		w.saveCommandForm()
	case id == "list":
		w.pickCommand()
	case strings.HasPrefix(id, "cmd"):
		i, _ := strconv.Atoi(strings.TrimPrefix(id, "cmd"))
		if i < len(w.commands) {
			w.runCommand(w.commands[i])
		}
	}
}

// runCommand types c into the focused pane without pressing Enter, so it
// can be checked or edited first.
func (w *Workspace) runCommand(c store.Command) {
	w.restore()
	if w.focused == nil {
		return
	}
	w.focused.pane.Send([]byte(c.Cmd))
	go func() { _ = w.t.store.TouchCommand(c.ID) }()
}

// restore puts the workspace back on screen after a picker or form.
func (w *Workspace) restore() {
	w.t.app.SetRoot(w.view, true)
	w.focusCurrent()
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
			w.flash(err.Error())
			return
		}
		w.reloadCommands()
		w.restore()
	})
	form.AddButton("Cancel", w.restore)
	form.SetCancelFunc(w.restore)
	w.t.app.SetRoot(centered(form, 90, 9), true).SetFocus(form)
}

// flash shows msg in the workspace status line for a few seconds.
func (w *Workspace) flash(msg string) {
	w.restore()
	w.status.SetText("[#FF6B6B]" + tview.Escape(msg) + "[-]")
	time.AfterFunc(3*time.Second, func() { w.t.app.QueueUpdateDraw(w.updateStatus) })
}

func (w *Workspace) pickCommand() {
	w.reloadCommands()
	if len(w.commands) == 0 {
		w.flash("No saved commands yet — Ctrl+B a saves one")
		return
	}
	items := make([]pickItem, len(w.commands))
	for i, c := range w.commands {
		sec := c.Cmd
		if c.Name == "" {
			sec = ""
		}
		items[i] = pickItem{main: c.Label(), secondary: sec, search: c.Name + " " + c.Cmd}
	}
	w.showPicker(" Saved commands — Enter types it in the pane, Ctrl+D deletes ", items, func(i int) {
		w.runCommand(w.commands[i])
	}, func(i int) {
		_ = w.t.store.DeleteCommand(w.commands[i].ID)
		w.reloadCommands()
		w.pickCommand()
	})
}

// pickServer asks which server to open in a new pane.
func (w *Workspace) pickServer(dir int) {
	servers := w.t.store.List()
	sortServers(servers, SortByLastSeenDesc)
	items := []pickItem{{main: "Local shell", secondary: "this machine", search: "local shell"}}
	for _, s := range servers {
		items = append(items, pickItem{main: s.Alias, secondary: s.Target(), search: s.Alias + " " + s.Target() + " " + strings.Join(s.Tags, " ")})
	}
	w.showPicker(" Open in new pane ", items, func(i int) {
		w.restore()
		if i == 0 {
			w.open(nil, dir)
			return
		}
		s := servers[i-1]
		w.open(&s, dir)
	}, nil)
}

type pickItem struct{ main, secondary, search string }

// showPicker is a filterable list. onDelete may be nil.
func (w *Workspace) showPicker(title string, items []pickItem, onPick, onDelete func(int)) {
	input := tview.NewInputField().SetLabel(" 🔍 ").SetFieldBackgroundColor(tcell.Color235)
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
			list.AddItem(it.main, it.secondary, 0, nil)
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
	list.SetSelectedFunc(func(int, string, string, rune) { pick() })
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(input, 1, 0, true).AddItem(list, 0, 1, false)
	box.SetBorder(true).SetTitle(title)
	w.t.app.SetRoot(centered(box, 80, 20), true).SetFocus(input)
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
