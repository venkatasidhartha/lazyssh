package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Full screen ("zoom"): one session fills the whole terminal, with a slim
// bar to leave full screen again. It works for tiles and popups alike.

func (w *Workspace) isLive(n *wsNode) bool {
	return indexOf(w.leaves(), n) >= 0 || w.floatOf(n) != nil
}

// zoom shows n full screen and types into it.
func (w *Workspace) zoom(n *wsNode) {
	if !w.isLive(n) {
		return
	}
	if f := w.floatOf(n); f != nil {
		f.minimized = false
		w.floatFocus = f
	} else {
		w.focused, w.floatFocus = n, nil
	}
	w.zoomed = n
	w.mode = modeTerminal
	w.showZoom()
}

func (w *Workspace) showZoom() {
	n := w.zoomed
	w.zoomBar.SetText(" [black:#55D7FF:b] ⤢ " + tview.Escape(n.pane.Label()) + " [-:-:-] [#8a8a8a]full screen[-]  " +
		btn("unzoom", "⤡ Exit full screen", "#87AFD7") +
		btn("rename", "✎ Rename", "#D7D7AF") +
		btn("cmds", "★ Commands", "#FFD75F") +
		btn("close", "✕ Close", "#FF8787"))
	w.zoomView.Clear()
	w.zoomView.AddItem(w.zoomBar, 1, 0, false).AddItem(n.pane, 0, 1, true)
	w.styleBorders()
	w.t.app.SetRoot(w.zoomView, true).SetFocus(n.pane)
}

func (w *Workspace) unzoom() {
	if w.zoomed == nil {
		return
	}
	w.zoomed = nil
	w.restore()
}

// toggleZoom zooms n, or leaves full screen if n is already zoomed.
func (w *Workspace) toggleZoom(n *wsNode) {
	if n == nil {
		return
	}
	if w.zoomed == n {
		w.unzoom()
		return
	}
	w.zoom(n)
}

// activeNode is the session being typed into (tile or popup).
func (w *Workspace) activeNode() *wsNode {
	if f := w.activeFloat(); f != nil {
		return f.node
	}
	return w.focused
}

// renameForm asks for a new name for n, shown in its title and the list.
func (w *Workspace) renameForm(n *wsNode) {
	if n == nil || !w.isLive(n) {
		return
	}
	w.mode = modeTerminal
	input := tview.NewInputField().SetLabel(" Name ").SetText(n.pane.Label()).SetFieldWidth(40).
		SetFieldBackgroundColor(tcell.Color235)
	done := func() {
		if w.zoomed != nil {
			w.showZoom()
			return
		}
		w.restore()
	}
	input.SetDoneFunc(func(k tcell.Key) {
		if k == tcell.KeyEnter {
			if name := strings.TrimSpace(input.GetText()); name != "" {
				n.pane.SetLabel(name)
			}
		}
		done()
	})
	hint := tview.NewTextView().SetDynamicColors(true).
		SetText(" [#8a8a8a]e.g. app logs, db shell, build · Enter saves · Esc cancels[-]")
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(input, 1, 0, true).AddItem(hint, 1, 0, false)
	box.SetBorder(true).SetTitle(" Rename session ")
	w.overlay(box, 60, 4, input)
}

// evenSizes gives every split pane the same share again.
func (w *Workspace) evenSizes() {
	var walk func(n *wsNode)
	walk = func(n *wsNode) {
		if n == nil {
			return
		}
		n.weight = 1000
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(w.root)
	w.refresh()
}
