package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// floatWin is a session in a popup window above the tiled panes. It can be
// moved by its title bar, resized from its bottom-right corner, minimized
// to the bottom bar, maximized, docked into the tiles or closed.
type floatWin struct {
	node      *wsNode
	x, y      int // offset from the top-left corner of the float layer
	w, h      int
	minimized bool
	maximized bool
}

// floatLayer draws the tiled body and the popup windows on top of it.
type floatLayer struct {
	*tview.Box
	ws   *Workspace
	body tview.Primitive

	hits    []paneHit
	pressed *paneHit
	swallow bool

	// A window being moved (resize false) or resized from its corner.
	drag   *floatWin
	resize bool
	dragDX int
	dragDY int
}

func newFloatLayer(ws *Workspace, body tview.Primitive) *floatLayer {
	return &floatLayer{Box: tview.NewBox(), ws: ws, body: body}
}

const minFloatW, minFloatH = 20, 5

// rect is where f is drawn, kept inside the layer.
func (l *floatLayer) rect(f *floatWin) (int, int, int, int) {
	lx, ly, lw, lh := l.GetRect()
	if f.maximized {
		return lx, ly, lw, lh
	}
	w := max(min(f.w, lw), min(minFloatW, lw))
	h := max(min(f.h, lh), min(minFloatH, lh))
	x := max(0, min(f.x, lw-w))
	y := max(0, min(f.y, lh-h))
	return lx + x, ly + y, w, h
}

func floatButtons(f *floatWin) []paneButton {
	maxi := paneButton{"fmax", "□ Maximize", "□", "#87AFD7"}
	if f.maximized {
		maxi = paneButton{"fmax", "❐ Restore", "❐", "#87AFD7"}
	}
	return []paneButton{
		{"frename", "✎ Rename", "✎", "#D7D7AF"},
		{"fmin", "▁ Minimize", "▁", "#D7D7AF"},
		maxi,
		{"fzoom", "⤢ Full screen", "⤢", "#87AFD7"},
		{"fdock", "⊞ Dock", "⊞", "#87D7AF"},
		{"fclose", "✕ Close", "✕", "#FF8787"},
	}
}

func (l *floatLayer) Draw(screen tcell.Screen) {
	x, y, w, h := l.GetRect()
	l.body.SetRect(x, y, w, h)
	l.body.Draw(screen)
	l.hits = l.hits[:0]
	for _, f := range l.ws.floats {
		if f.minimized {
			continue
		}
		fx, fy, fw, fh := l.rect(f)
		f.node.pane.SetRect(fx, fy, fw, fh)
		f.node.pane.Draw(screen)
		pressed := ""
		if l.pressed != nil && l.pressed.node == f.node {
			pressed = l.pressed.id
		}
		l.hits = append(l.hits, drawTitleButtons(screen, f.node, floatButtons(f), pressed)...)
		// Resize grip in the bottom-right corner.
		if !f.maximized {
			screen.SetContent(fx+fw-1, fy+fh-1, '◢', nil, tcell.StyleDefault.Foreground(tcell.ColorGray))
		}
	}
}

// Focus goes to the session that was last used.
func (l *floatLayer) Focus(delegate func(tview.Primitive)) {
	if f := l.ws.activeFloat(); f != nil {
		delegate(f.node.pane)
		return
	}
	delegate(l.body)
}

func (l *floatLayer) HasFocus() bool {
	for _, f := range l.ws.floats {
		if f.node.pane.HasFocus() {
			return true
		}
	}
	return l.body.HasFocus()
}

// Keys travel down from the root to the focused primitive.
func (l *floatLayer) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return func(ev *tcell.EventKey, setFocus func(tview.Primitive)) {
		for _, f := range l.ws.floats {
			if f.node.pane.HasFocus() {
				f.node.pane.InputHandler()(ev, setFocus)
				return
			}
		}
		if h := l.body.InputHandler(); h != nil {
			h(ev, setFocus)
		}
	}
}

func (l *floatLayer) PasteHandler() func(string, func(tview.Primitive)) {
	return func(text string, setFocus func(tview.Primitive)) {
		for _, f := range l.ws.floats {
			if f.node.pane.HasFocus() {
				f.node.pane.PasteHandler()(text, setFocus)
				return
			}
		}
		if h := l.body.PasteHandler(); h != nil {
			h(text, setFocus)
		}
	}
}

// floatAt is the topmost visible window under x, y.
func (l *floatLayer) floatAt(x, y int) *floatWin {
	for i := len(l.ws.floats) - 1; i >= 0; i-- {
		f := l.ws.floats[i]
		if f.minimized {
			continue
		}
		fx, fy, fw, fh := l.rect(f)
		if x >= fx && x < fx+fw && y >= fy && y < fy+fh {
			return f
		}
	}
	return nil
}

func (l *floatLayer) hitButton(x, y int) *paneHit {
	// Later hits are drawn on top.
	for i := len(l.hits) - 1; i >= 0; i-- {
		if h := &l.hits[i]; y == h.y && x >= h.x0 && x <= h.x1 {
			return h
		}
	}
	return nil
}

func (l *floatLayer) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return l.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		x, y := ev.Position()
		switch {
		case l.drag != nil:
			if action == tview.MouseLeftUp {
				l.drag = nil
				l.ws.saveLayout()
				return true, nil
			}
			if action == tview.MouseMove {
				l.dragTo(x, y)
			}
			return true, l
		case l.pressed != nil:
			if action != tview.MouseLeftUp {
				return true, l
			}
			p := l.pressed
			l.pressed, l.swallow = nil, true
			if h := l.hitButton(x, y); h != nil && h.id == p.id && h.node == p.node {
				if f := l.ws.floatOf(p.node); f != nil {
					l.ws.floatButton(f, p.id)
				}
			}
			return true, nil
		case l.swallow && (action == tview.MouseLeftClick || action == tview.MouseLeftDoubleClick):
			l.swallow = false
			return true, nil
		}
		l.swallow = false

		f := l.floatAt(x, y)
		if f == nil {
			if h := l.body.MouseHandler(); h != nil {
				return h(action, ev, setFocus)
			}
			return false, nil
		}
		fx, fy, fw, fh := l.rect(f)
		if action == tview.MouseLeftDown {
			l.ws.focusFloat(f)
			if h := l.hitButton(x, y); h != nil {
				p := *h
				l.pressed = &p
				return true, l
			}
			switch {
			case !f.maximized && x >= fx+fw-2 && y == fy+fh-1:
				l.drag, l.resize = f, true
				return true, l
			case y == fy && !f.maximized:
				l.drag, l.resize = f, false
				l.dragDX, l.dragDY = x-fx, y-fy
				return true, l
			}
		}
		if action == tview.MouseLeftDoubleClick && y == fy {
			l.ws.zoom(f.node)
			return true, nil
		}
		if h := f.node.pane.MouseHandler(); h != nil {
			if consumed, capture := h(action, ev, setFocus); capture != nil {
				return consumed, capture
			}
		}
		return true, nil // the window covers whatever is below it
	})
}

func (l *floatLayer) dragTo(x, y int) {
	f := l.drag
	lx, ly, lw, lh := l.GetRect()
	fx, fy, _, _ := l.rect(f)
	if l.resize {
		f.w = max(minFloatW, min(x-fx+1, lx+lw-fx))
		f.h = max(minFloatH, min(y-fy+1, ly+lh-fy))
		return
	}
	f.x = max(0, min(x-l.dragDX-lx, lw-f.w))
	f.y = max(0, min(y-l.dragDY-ly, lh-f.h))
}
