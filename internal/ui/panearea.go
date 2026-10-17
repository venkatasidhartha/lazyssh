package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// paneArea holds the session panes. On top of the panes it draws a few
// buttons in each pane's title bar (split, maximize, close) and lets the
// border between two panes be dragged to resize them.
type paneArea struct {
	*tview.Flex
	w *Workspace

	hits    []paneHit // buttons drawn in the last Draw
	pressed *paneHit  // button under a held mouse button
	swallow bool      // eat the click that follows a button release
	drag    *paneDrag
}

type paneHit struct {
	x0, x1, y int
	id        string
	node      *wsNode
}

// paneDrag is a border being dragged: between split.kids[i] and kids[i+1].
type paneDrag struct {
	split *wsNode
	i     int
}

func newPaneArea(w *Workspace) *paneArea {
	return &paneArea{Flex: tview.NewFlex(), w: w}
}

type paneButton struct{ id, wide, narrow, bg string }

func (a *paneArea) buttons() []paneButton {
	return []paneButton{
		{"rename", "✎ Rename", "✎", "#D7D7AF"},
		{"splitv", "⇆ Split right", "⇆", "#87D7AF"},
		{"splith", "⇅ Split down", "⇅", "#87D7AF"},
		{"float", "❐ Pop out", "❐", "#D7AFFF"},
		{"zoom", "⤢ Full screen", "⤢", "#87AFD7"},
		{"close", "✕ Close", "✕", "#FF8787"},
	}
}

func (a *paneArea) Draw(screen tcell.Screen) {
	a.Flex.Draw(screen)
	a.hits = a.hits[:0]
	leaves := a.w.leaves()
	if a.w.tabs && a.w.focused != nil {
		leaves = []*wsNode{a.w.focused}
	}
	btns := a.buttons()
	for _, n := range leaves {
		pressed := ""
		if a.pressed != nil && a.pressed.node == n {
			pressed = a.pressed.id
		}
		a.hits = append(a.hits, drawTitleButtons(screen, n, btns, pressed)...)
	}
}

// drawTitleButtons draws btns right-aligned on the top border of n's pane
// and returns where they are.
func drawTitleButtons(screen tcell.Screen, n *wsNode, btns []paneButton, pressed string) []paneHit {
	var hits []paneHit
	x, y, width, _ := n.pane.GetRect()
	// Full labels where they fit next to the title; the longest ones
	// shrink to their icon first.
	labels := make([]string, len(btns))
	total := 0
	for i, b := range btns {
		labels[i] = " " + b.wide + " "
		total += len([]rune(labels[i])) + 1
	}
	room := width - len([]rune(n.pane.GetTitle())) - 4
	for total > room {
		longest := -1
		for i, b := range btns {
			if labels[i] != " "+b.narrow+" " && (longest < 0 || len([]rune(labels[i])) > len([]rune(labels[longest]))) {
				longest = i
			}
		}
		if longest < 0 {
			break
		}
		total -= len([]rune(labels[longest])) - len([]rune(btns[longest].narrow)) - 2
		labels[longest] = " " + btns[longest].narrow + " "
	}
	right := x + width - 2
	for i := len(btns) - 1; i >= 0; i-- {
		b := btns[i]
		label := labels[i]
		lw := len([]rune(label))
		x0 := right - lw + 1
		if x0 <= x+1 {
			break
		}
		style := tcell.StyleDefault.Foreground(tcell.ColorBlack).Background(tcell.GetColor(b.bg))
		if pressed == b.id {
			style = style.Background(tcell.ColorWhite)
		}
		for i, r := range []rune(label) {
			screen.SetContent(x0+i, y, r, nil, style)
		}
		hits = append(hits, paneHit{x0, right, y, b.id, n})
		right = x0 - 2
	}
	return hits
}

func (a *paneArea) hitButton(x, y int) *paneHit {
	for i := range a.hits {
		if h := &a.hits[i]; y == h.y && x >= h.x0 && x <= h.x1 {
			return h
		}
	}
	return nil
}

// hitBorder finds the border between two side-by-side (or stacked) panes
// at x, y. Each pane draws its own border, so either line counts.
func (a *paneArea) hitBorder(x, y int) *paneDrag {
	if a.w.tabs {
		return nil
	}
	var found *paneDrag
	var walk func(n *wsNode)
	walk = func(n *wsNode) {
		if n == nil || n.pane != nil || found != nil {
			return
		}
		for i := 0; i+1 < len(n.kids); i++ {
			ax, ay, aw, ah := n.kids[i].prim.GetRect()
			bx, by, _, _ := n.kids[i+1].prim.GetRect()
			_, _, sw, sh := n.prim.GetRect()
			if n.dir == tview.FlexColumn {
				if (x == ax+aw-1 || x == bx) && y >= ay && y < ay+sh {
					found = &paneDrag{n, i}
					return
				}
			} else if (y == ay+ah-1 || y == by) && x >= ax && x < ax+sw {
				found = &paneDrag{n, i}
				return
			}
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(a.w.root)
	if found != nil {
		// Make the weights match the current sizes so the drag is exact.
		for _, k := range found.split.kids {
			_, _, kw, kh := k.prim.GetRect()
			k.weight = max(1, kw)
			if found.split.dir == tview.FlexRow {
				k.weight = max(1, kh)
			}
		}
	}
	return found
}

const minPaneCols, minPaneRows = 12, 4

func (a *paneArea) dragTo(x, y int) {
	d := a.drag
	ka, kb := d.split.kids[d.i], d.split.kids[d.i+1]
	ax, ay, aw, ah := ka.prim.GetRect()
	_, _, bw, bh := kb.prim.GetRect()
	total, size, minSize := aw+bw, x-ax+1, minPaneCols
	if d.split.dir == tview.FlexRow {
		total, size, minSize = ah+bh, y-ay+1, minPaneRows
	}
	if total < 2*minSize {
		return
	}
	size = max(minSize, min(size, total-minSize))
	ka.weight, kb.weight = size, total-size
	f := d.split.prim.(*tview.Flex)
	f.ResizeItem(ka.prim, 0, ka.weight)
	f.ResizeItem(kb.prim, 0, kb.weight)
}

func (a *paneArea) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return a.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		x, y := ev.Position()
		switch {
		case a.drag != nil:
			if action == tview.MouseLeftUp {
				a.drag = nil
				a.w.saveLayout()
				return true, nil
			}
			if action == tview.MouseMove {
				a.dragTo(x, y)
			}
			return true, a
		case a.pressed != nil:
			if action != tview.MouseLeftUp {
				return true, a
			}
			p := a.pressed
			a.pressed, a.swallow = nil, true
			if h := a.hitButton(x, y); h != nil && h.id == p.id && h.node == p.node {
				a.w.paneButton(p.node, p.id)
			}
			return true, nil
		case a.swallow && (action == tview.MouseLeftClick || action == tview.MouseLeftDoubleClick):
			a.swallow = false
			return true, nil
		}
		a.swallow = false
		// Double-click on a title bar: full screen.
		if action == tview.MouseLeftDoubleClick {
			for _, n := range a.w.leaves() {
				px, py, pw, _ := n.pane.GetRect()
				if y == py && x >= px && x < px+pw && (!a.w.tabs || n == a.w.focused) {
					a.w.zoom(n)
					return true, nil
				}
			}
		}
		if action == tview.MouseLeftDown {
			if h := a.hitButton(x, y); h != nil {
				p := *h
				a.pressed = &p
				return true, a
			}
			if d := a.hitBorder(x, y); d != nil {
				a.drag = d
				return true, a
			}
		}
		return a.Flex.MouseHandler()(action, ev, setFocus)
	})
}
