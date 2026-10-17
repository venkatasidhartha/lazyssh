package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/store"
)

// ---------------------------------------------------------------------------
// Dialogs over the workspace
// ---------------------------------------------------------------------------

// modal shows a dialog in the middle of the screen with the workspace still
// visible behind it. Clicks outside the dialog do not reach the workspace.
type modal struct {
	*tview.Flex
	box tview.Primitive
}

func (m *modal) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		x, y := ev.Position()
		bx, by, bw, bh := m.box.GetRect()
		if x >= bx && x < bx+bw && y >= by && y < by+bh {
			return m.box.MouseHandler()(action, ev, setFocus)
		}
		return true, nil
	}
}

// overlay puts p (width x height) over the workspace and focuses focus.
func (w *Workspace) overlay(p tview.Primitive, width, height int, focus tview.Primitive) {
	bg := tview.Primitive(w.view)
	if w.zoomed != nil {
		bg = w.zoomView
	}
	m := &modal{Flex: centered(p, width, height).(*tview.Flex), box: p}
	pages := tview.NewPages().
		AddPage("workspace", bg, true, true).
		AddPage("dialog", m, true, true)
	w.t.app.SetRoot(pages, true).SetFocus(focus)
}

// back closes a dialog: to full screen if a session is zoomed, else to the
// workspace.
func (w *Workspace) back() {
	if w.zoomed != nil && w.isLive(w.zoomed) {
		w.showZoom()
		return
	}
	w.restore()
}

// ---------------------------------------------------------------------------
// Saved commands
// ---------------------------------------------------------------------------

// commandManager is the ★ Commands window: the saved commands with
// buttons to type, run, add, edit and remove them.
func (w *Workspace) commandManager() {
	w.mode = modeTerminal
	w.reloadCommands()
	cmds := w.commands // what the list shows, even if the file changes

	list := tview.NewList().ShowSecondaryText(true).SetSecondaryTextColor(tcell.Color245).
		SetHighlightFullLine(true).SetSelectedBackgroundColor(tcell.Color238).SetSelectedTextColor(tcell.Color255)
	for i, c := range cmds {
		num := "  "
		if i < 9 {
			num = fmt.Sprintf("%d ", i+1)
		}
		sec := "    " + tview.Escape(c.Cmd)
		if c.Name == "" {
			sec = ""
		}
		list.AddItem("[#FFD75F]"+num+"[-]"+tview.Escape(c.Label()), sec, 0, nil)
	}
	if len(cmds) == 0 {
		list.AddItem("[#8a8a8a]No saved commands yet — click + New[-]", "", 0, nil)
	}
	current := func() (store.Command, bool) {
		i := list.GetCurrentItem()
		if i < 0 || i >= len(cmds) {
			return store.Command{}, false
		}
		return cmds[i], true
	}

	target := "the active session"
	if p := w.activePane(); p != nil {
		target = p.Label()
	}
	bar := w.buttonBar()
	info := tview.NewTextView().SetDynamicColors(true)
	info.SetText(fmt.Sprintf(" [#8a8a8a]▶ Type puts it in [white]%s[#8a8a8a] without Enter so you can check it · ↵ Run also presses Enter[-]", tview.Escape(target)))
	setBar := func(confirm string) {
		if confirm != "" {
			bar.SetText(" [#FF8787]Remove " + tview.Escape(truncate(confirm, 40)) + "?[-]  " +
				btn("mdelyes", "✕ Yes, remove", "#FF8787") + btn("mdelno", "Keep it", "#8A8A8A"))
			return
		}
		bar.SetText(" " + btn("mtype", "▶ Type", "#55D7FF") + btn("mrun", "↵ Run", "#87D7AF") +
			btn("mnew", "+ New", "#FFD75F") + btn("medit", "✎ Edit", "#D7D7AF") +
			btn("mdel", "✕ Remove", "#FF8787") + btn("mclose", "Close", "#8A8A8A"))
	}
	setBar("")

	run := func(enter bool) {
		c, ok := current()
		if !ok {
			return
		}
		w.back()
		w.runCommand(c)
		if p := w.activePane(); enter && p != nil {
			p.Type("\r")
		}
	}
	act := func(id string) {
		switch id {
		case "mtype":
			run(false)
		case "mrun":
			run(true)
		case "mnew":
			w.commandForm(nil)
		case "medit":
			if c, ok := current(); ok {
				w.commandForm(&c)
			}
		case "mdel":
			if c, ok := current(); ok {
				setBar(c.Label())
			}
		case "mdelyes":
			if c, ok := current(); ok {
				if err := w.t.store.DeleteCommand(c.ID); err != nil {
					w.back()
					w.flash(err.Error(), "#FF6B6B")
					return
				}
				w.commandManager() // fresh list
				w.flash("Removed "+c.Label(), "#A0FFA0")
			}
		case "mdelno":
			setBar("")
		case "mclose":
			w.back()
		}
	}
	// The bar's buttons call w.button; route this window's ids here.
	w.dialogButton = act
	list.SetSelectedFunc(func(int, string, string, rune) { run(false) })
	keys := func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyEsc:
			w.back()
		case ev.Key() == tcell.KeyDelete || ev.Rune() == 'd':
			act("mdel")
		case ev.Rune() == 'y' && strings.Contains(bar.GetText(true), "Yes, remove"):
			act("mdelyes")
		case ev.Rune() == 'n':
			act("mnew")
		case ev.Rune() == 'e':
			act("medit")
		case ev.Rune() == 'r':
			act("mrun")
		default:
			return ev
		}
		return nil
	}
	list.SetInputCapture(keys)
	list.SetChangedFunc(func(int, string, string, rune) { setBar("") })

	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(info, 1, 0, false).
		AddItem(bar, 1, 0, false)
	box.SetBorder(true).SetTitle(" ★ Saved commands ").SetBorderColor(tcell.Color221)
	w.overlay(box, 92, 22, list)
}

// commandForm adds a command (c == nil) or edits c, then goes back to the
// command window.
func (w *Workspace) commandForm(c *store.Command) {
	w.mode = modeTerminal
	name, cmd, title := "", "", " + New command "
	if c != nil {
		name, cmd, title = c.Name, c.Cmd, " ✎ Edit command "
	} else if p := w.activePane(); p != nil {
		cmd = promptGuess(p.CursorLine()) // what is being typed, if anything
	}
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(title).SetTitleAlign(tview.AlignCenter).SetBorderColor(tcell.Color221)
	form.AddInputField("Command", cmd, 70, nil, nil)
	form.AddInputField("Name", name, 30, nil, nil)
	cmdField := form.GetFormItem(0).(*tview.InputField)
	nameField := form.GetFormItem(1).(*tview.InputField)
	cmdField.SetPlaceholder("e.g. tail -f /var/log/syslog").SetPlaceholderTextColor(tcell.Color242)
	nameField.SetPlaceholder("optional short label, e.g. app logs").SetPlaceholderTextColor(tcell.Color242)
	save := func() {
		var err error
		if c == nil {
			_, err = w.t.store.AddCommand(nameField.GetText(), cmdField.GetText())
		} else {
			err = w.t.store.UpdateCommand(c.ID, nameField.GetText(), cmdField.GetText())
		}
		if err != nil {
			form.SetTitle(" " + err.Error() + " ")
			return
		}
		w.reloadCommands()
		w.commandManager()
	}
	form.AddButton("Save", save)
	form.AddButton("Cancel", w.commandManager)
	form.SetCancelFunc(w.commandManager)
	w.overlay(form, 90, 9, form)
}
