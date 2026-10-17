package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/store"
)

// Session groups: the open sessions (servers, local shells, names, splits
// and popups) saved under a name, to open them all again with one click.

// groupLayout is the current layout as a group: what to open, not which
// running sessions to reattach.
func (w *Workspace) groupLayout() (savedLayout, []string) {
	l := w.snapshot()
	var names []string
	var strip func(n *savedNode)
	strip = func(n *savedNode) {
		n.Session = ""
		if len(n.Kids) == 0 {
			names = append(names, n.Label)
		}
		for i := range n.Kids {
			strip(&n.Kids[i])
		}
	}
	if l.Root != nil {
		strip(l.Root)
	}
	for i := range l.Floats {
		strip(&l.Floats[i].Node)
	}
	l.Zoomed, l.Tabs, l.NoList = "", false, false
	return l, names
}

// openGroup opens every session of g. In an empty workspace it gets g's
// layout; otherwise it is put next to the sessions already open.
func (w *Workspace) openGroup(g store.Group) {
	var l savedLayout
	if err := json.Unmarshal(g.Layout, &l); err != nil {
		w.flash("Cannot open "+g.Name+": "+err.Error(), "#FF6B6B")
		return
	}
	w.restoring = true
	var first *wsNode
	var tree *wsNode
	if l.Root != nil {
		tree = w.buildSaved(*l.Root, nil, &first, true)
	}
	if tree != nil {
		if w.root == nil {
			tree.parent = nil
			w.root = tree
		} else {
			old := w.root
			split := &wsNode{dir: w.autoDir(), weight: 1000}
			old.parent, tree.parent = split, split
			old.weight, tree.weight = 1000, 1000
			split.kids = []*wsNode{old, tree}
			w.root = split
		}
	}
	var lastFloat *floatWin
	for _, sf := range l.Floats {
		var dummy *wsNode
		n := w.buildSaved(sf.Node, nil, &dummy, true)
		if n == nil || n.pane == nil {
			continue
		}
		f := &floatWin{node: n, x: sf.X, y: sf.Y, w: max(sf.W, minFloatW), h: max(sf.H, minFloatH),
			minimized: sf.Minimized, maximized: sf.Maximized}
		w.floats = append(w.floats, f)
		if !f.minimized {
			lastFloat = f
		}
	}
	w.restoring = false
	w.zoomed = nil
	switch {
	case first != nil:
		w.focused, w.floatFocus = first, nil
	case lastFloat != nil:
		w.floatFocus = lastFloat
	}
	go func() { _ = w.t.store.TouchGroup(g.ID) }()
	w.show()
	w.flash(fmt.Sprintf("Opened group %s", g.Name), "#A0FFA0")
}

// groupManager is the ▣ Groups window.
func (w *Workspace) groupManager() {
	w.mode = modeTerminal
	groups, err := w.t.store.Groups()
	if err != nil {
		w.flash(err.Error(), "#FF6B6B")
		return
	}
	list := tview.NewList().ShowSecondaryText(true).SetSecondaryTextColor(tcell.Color245).
		SetHighlightFullLine(true).SetSelectedBackgroundColor(tcell.Color238).SetSelectedTextColor(tcell.Color255)
	for _, g := range groups {
		list.AddItem("[#87D7AF]▣[-] "+tview.Escape(g.Name), "    "+tview.Escape(truncate(fmt.Sprintf("%d session%s: %s", len(g.Sessions), plural(len(g.Sessions)), strings.Join(g.Sessions, ", ")), 84)), 0, nil)
	}
	if len(groups) == 0 {
		list.AddItem("[#8a8a8a]No groups yet — open the sessions you want together, then click + Save open sessions[-]", "", 0, nil)
	}
	current := func() (store.Group, bool) {
		i := list.GetCurrentItem()
		if i < 0 || i >= len(groups) {
			return store.Group{}, false
		}
		return groups[i], true
	}
	info := tview.NewTextView().SetDynamicColors(true).
		SetText(" [#8a8a8a]A group opens all its sessions at once, laid out as they were saved[-]")
	bar := w.buttonBar()
	setBar := func(confirm string) {
		if confirm != "" {
			bar.SetText(" [#FF8787]Remove group " + tview.Escape(truncate(confirm, 30)) + "? (its sessions are not closed)[-] " +
				btn("mgdelyes", "✕ Yes, remove", "#FF8787") + btn("mgdelno", "Keep it", "#8A8A8A"))
			return
		}
		bar.SetText(" " + btn("mgopen", "▶ Open", "#55D7FF") + btn("mgsave", "+ Save open sessions", "#FFD75F") +
			btn("mgrename", "✎ Rename", "#D7D7AF") + btn("mgdel", "✕ Remove", "#FF8787") + btn("mgclose", "Close", "#8A8A8A"))
	}
	setBar("")
	act := func(id string) {
		switch id {
		case "mgopen":
			if g, ok := current(); ok {
				w.openGroup(g)
			}
		case "mgsave":
			w.saveGroupForm()
		case "mgrename":
			if g, ok := current(); ok {
				w.renameGroupForm(g)
			}
		case "mgdel":
			if g, ok := current(); ok {
				setBar(g.Name)
			}
		case "mgdelyes":
			if g, ok := current(); ok {
				_ = w.t.store.DeleteGroup(g.ID)
				w.groupManager()
				w.flash("Removed group "+g.Name, "#A0FFA0")
			}
		case "mgdelno":
			setBar("")
		case "mgclose":
			w.back()
		}
	}
	w.dialogButton = act
	list.SetSelectedFunc(func(int, string, string, rune) { act("mgopen") })
	list.SetChangedFunc(func(int, string, string, rune) { setBar("") })
	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyEsc:
			w.back()
		case ev.Key() == tcell.KeyDelete || ev.Rune() == 'd':
			act("mgdel")
		case ev.Rune() == 's' || ev.Rune() == 'n':
			act("mgsave")
		case ev.Rune() == 'r':
			act("mgrename")
		default:
			return ev
		}
		return nil
	})
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(info, 1, 0, false).
		AddItem(bar, 1, 0, false)
	box.SetBorder(true).SetTitle(" ▣ Session groups ").SetBorderColor(tcell.Color114)
	w.overlay(box, 96, 20, list)
}

// saveGroupForm saves the open sessions as a group.
func (w *Workspace) saveGroupForm() {
	l, names := w.groupLayout()
	if len(names) == 0 {
		w.back()
		w.flash("Open the sessions you want in the group first", "#FFD75F")
		return
	}
	data, _ := json.Marshal(l)
	input := tview.NewInputField().SetLabel(" Group name ").SetFieldWidth(40).
		SetFieldBackgroundColor(tcell.Color235).
		SetPlaceholder("e.g. prod monitoring").SetPlaceholderTextColor(tcell.Color242)
	hint := tview.NewTextView().SetDynamicColors(true).
		SetText(" [#8a8a8a]Saves " + tview.Escape(truncate(strings.Join(names, ", "), 70)) + " · Enter saves · Esc cancels[-]")
	input.SetDoneFunc(func(k tcell.Key) {
		if k != tcell.KeyEnter {
			w.groupManager()
			return
		}
		if _, err := w.t.store.SaveGroup(input.GetText(), data, names); err != nil {
			hint.SetText(" [#FF8787]" + tview.Escape(err.Error()) + "[-]")
			return
		}
		w.groupManager()
		w.flash("Saved group "+strings.TrimSpace(input.GetText()), "#A0FFA0")
	})
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(input, 1, 0, true).AddItem(hint, 1, 0, false)
	box.SetBorder(true).SetTitle(" + Save open sessions as a group ").SetBorderColor(tcell.Color114)
	w.overlay(box, 90, 4, input)
}

func (w *Workspace) renameGroupForm(g store.Group) {
	input := tview.NewInputField().SetLabel(" Group name ").SetText(g.Name).SetFieldWidth(40).
		SetFieldBackgroundColor(tcell.Color235)
	input.SetDoneFunc(func(k tcell.Key) {
		if k == tcell.KeyEnter {
			_ = w.t.store.RenameGroup(g.ID, input.GetText())
		}
		w.groupManager()
	})
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(input, 1, 0, true)
	box.SetBorder(true).SetTitle(" ✎ Rename group ").SetBorderColor(tcell.Color114)
	w.overlay(box, 60, 3, input)
}
