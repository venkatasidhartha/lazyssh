// Package vterm embeds terminal sessions in the TUI: a Pane is a tview
// primitive that emulates a VT/xterm screen and is fed by a Backend (a
// local PTY or an SSH session).
package vterm

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gdamore/tcell/v2"
	"github.com/hinshun/vt10x"
	"github.com/rivo/tview"
)

// Backend is the program running in a pane.
type Backend interface {
	io.Writer // keystrokes to the program
	Resize(cols, rows int) error
	Close() error
}

// vt10x glyph attribute bits (unexported in the library).
const (
	attrUnderline = 1 << 1
	attrBold      = 1 << 2
	attrItalic    = 1 << 4
	attrBlink     = 1 << 5
)

type Pane struct {
	*tview.Box

	vt     vt10x.Terminal
	mu     sync.Mutex // guards be, cols, rows, exited
	be     Backend
	cols   int
	rows   int
	exited bool

	label   string
	redraw  func()      // schedules a screen redraw (coalesced)
	pending atomic.Bool // a redraw is already queued
	onExit  func(*Pane)
}

// NewPane creates an empty pane. redraw is called from background
// goroutines when the screen changed; it must be safe to call from any
// goroutine (e.g. tview.Application.Draw).
func NewPane(label string, redraw func()) *Pane {
	p := &Pane{Box: tview.NewBox(), label: label, cols: 80, rows: 24, redraw: redraw}
	// Terminal replies (cursor position reports etc.) go back to the program.
	p.vt = vt10x.New(vt10x.WithSize(p.cols, p.rows), vt10x.WithWriter(replyWriter{p}))
	p.SetBorder(true)
	p.updateTitle()
	return p
}

type replyWriter struct{ p *Pane }

func (r replyWriter) Write(b []byte) (int, error) {
	r.p.mu.Lock()
	be := r.p.be
	r.p.mu.Unlock()
	if be == nil {
		return len(b), nil
	}
	return be.Write(b)
}

func (p *Pane) Label() string { return p.label }

func (p *Pane) SetLabel(l string) {
	p.label = l
	p.updateTitle()
}

func (p *Pane) updateTitle() {
	t := " " + p.label + " "
	if title := p.vt.Title(); title != "" && title != p.label {
		t = " " + p.label + " — " + title + " "
	}
	p.SetTitle(t)
}

// OnExit is called (from a background goroutine) when the program ends.
func (p *Pane) OnExit(fn func(*Pane)) *Pane { p.onExit = fn; return p }

// Size is the current inner size in cells.
func (p *Pane) Size() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows
}

// Attach connects the pane to a running program.
func (p *Pane) Attach(be Backend) {
	p.mu.Lock()
	p.be = be
	cols, rows := p.cols, p.rows
	p.mu.Unlock()
	_ = be.Resize(cols, rows)
}

// Output is where a backend writes the program's output.
func (p *Pane) Output() io.Writer { return outputWriter{p} }

type outputWriter struct{ p *Pane }

func (o outputWriter) Write(b []byte) (int, error) {
	n, err := o.p.vt.Write(b)
	o.p.scheduleRedraw()
	return n, err
}

// Printf-style status line written straight into the screen (for
// "Connecting…" and error messages before/after a program runs).
func (p *Pane) Message(s string) {
	_, _ = p.vt.Write([]byte(s))
	p.scheduleRedraw()
}

func (p *Pane) scheduleRedraw() {
	if p.redraw != nil && p.pending.CompareAndSwap(false, true) {
		p.redraw()
	}
}

// Exited marks the program as finished.
func (p *Pane) Exited() {
	p.mu.Lock()
	p.exited = true
	p.be = nil
	p.mu.Unlock()
	p.Message("\r\n\x1b[38;5;245m[session ended — press Enter to close this pane]\x1b[0m")
	if p.onExit != nil {
		p.onExit(p)
	}
}

func (p *Pane) IsExited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited
}

// Close stops the program.
func (p *Pane) Close() {
	p.mu.Lock()
	be := p.be
	p.be = nil
	p.mu.Unlock()
	if be != nil {
		_ = be.Close()
	}
}

// Send writes raw bytes to the program (keystrokes, pasted commands).
func (p *Pane) Send(b []byte) {
	p.mu.Lock()
	be := p.be
	p.mu.Unlock()
	if be != nil {
		_, _ = be.Write(b)
	}
}

// CursorLine returns the text of the line the cursor is on, up to the
// cursor (what the user is typing at a shell prompt, prompt included).
func (p *Pane) CursorLine() string {
	p.vt.Lock()
	defer p.vt.Unlock()
	c := p.vt.Cursor()
	cols, _ := p.vt.Size()
	var b strings.Builder
	for x := 0; x < cols; x++ {
		ch := p.vt.Cell(x, c.Y).Char
		if ch == 0 {
			ch = ' '
		}
		b.WriteRune(ch)
	}
	return strings.TrimRight(b.String(), " ")
}

func (p *Pane) Draw(screen tcell.Screen) {
	p.pending.Store(false)
	p.updateTitle()
	p.Box.DrawForSubclass(screen, p)
	x, y, w, h := p.GetInnerRect()
	if w <= 0 || h <= 0 {
		return
	}
	// Never hold p.mu while taking the vt lock: the vt calls replyWriter
	// (which takes p.mu) with its own lock held.
	p.mu.Lock()
	resized := w != p.cols || h != p.rows
	p.cols, p.rows = w, h
	be := p.be
	p.mu.Unlock()
	if resized {
		p.vt.Resize(w, h)
		if be != nil {
			_ = be.Resize(w, h)
		}
	}

	bg := p.GetBackgroundColor()
	p.vt.Lock()
	defer p.vt.Unlock()
	cols, rows := p.vt.Size()
	for row := 0; row < rows && row < h; row++ {
		for col := 0; col < cols && col < w; col++ {
			g := p.vt.Cell(col, row)
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			screen.SetContent(x+col, y+row, ch, nil, glyphStyle(g, bg))
		}
	}
	if p.HasFocus() && p.vt.CursorVisible() {
		c := p.vt.Cursor()
		if c.X < w && c.Y < h {
			screen.ShowCursor(x+c.X, y+c.Y)
		}
	}
}

func glyphStyle(g vt10x.Glyph, bg tcell.Color) tcell.Style {
	st := tcell.StyleDefault.Foreground(toColor(g.FG, tcell.ColorDefault)).Background(toColor(g.BG, bg))
	if g.Mode&attrBold != 0 {
		st = st.Bold(true)
	}
	if g.Mode&attrUnderline != 0 {
		st = st.Underline(true)
	}
	if g.Mode&attrItalic != 0 {
		st = st.Italic(true)
	}
	if g.Mode&attrBlink != 0 {
		st = st.Blink(true)
	}
	return st
}

func toColor(c vt10x.Color, def tcell.Color) tcell.Color {
	switch {
	case c >= vt10x.DefaultFG:
		return def
	case c < 256:
		return tcell.PaletteColor(int(c))
	default:
		return tcell.NewRGBColor(int32(c>>16&0xff), int32(c>>8&0xff), int32(c&0xff))
	}
}

func (p *Pane) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return p.WrapInputHandler(func(ev *tcell.EventKey, _ func(tview.Primitive)) {
		p.Send(keyBytes(ev, p.vt.Mode()&vt10x.ModeAppCursor != 0))
	})
}

func (p *Pane) PasteHandler() func(string, func(tview.Primitive)) {
	return p.WrapPasteHandler(func(text string, _ func(tview.Primitive)) {
		p.Send([]byte(text))
	})
}

func (p *Pane) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return p.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if !p.InRect(ev.Position()) {
			return false, nil
		}
		if action == tview.MouseLeftDown {
			setFocus(p)
			return true, nil
		}
		return false, nil
	})
}
