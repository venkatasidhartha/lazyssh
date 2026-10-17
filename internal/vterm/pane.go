// Package vterm embeds terminal sessions in the TUI: a Pane is a tview
// primitive that emulates an xterm screen (with scrollback, mouse and
// selection) and is fed by a Backend (a local PTY or an SSH session).
package vterm

import (
	"image/color"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Backend is the program running in a pane.
type Backend interface {
	io.Writer // keystrokes to the program
	Resize(cols, rows int) error
	Close() error
}

const (
	scrollbackLines = 10000
	wheelLines      = 3
)

type Pane struct {
	*tview.Box

	emu    *vt.SafeEmulator
	mu     sync.Mutex // guards the fields below
	be     Backend
	cols   int
	rows   int
	exited bool

	// Viewport and selection, used on the UI goroutine only. Lines are
	// absolute: 0 is the oldest scrollback line, sbLen is the first
	// screen line.
	scroll    int // lines scrolled up into the scrollback
	lastSB    int // scrollback length at the last draw
	selecting bool
	fwdHeld   bool // a mouse button pressed in the program is still down

	// Input for the program is queued and written by its own goroutine:
	// a program that stops reading must not block the UI or the
	// emulator (whose lock is held while it encodes mouse events).
	inMu     sync.Mutex
	inCond   *sync.Cond
	inQ      [][]byte
	inClosed bool
	selA     [2]int // anchor (x, line)
	selB     [2]int // current end
	hasSel   bool

	// Terminal modes requested by the program (set from emulator callbacks).
	appCursor  atomic.Bool
	mouseModes atomic.Int32 // bit set of mouse tracking modes
	title      atomic.Value // string

	// scr guards emu's screen: the emulator hands out pointers to its
	// cells, so reads must not overlap a write.
	scr sync.RWMutex

	label   string
	redraw  func()
	pending atomic.Bool

	// OnCopy receives selected text; OnPasteRequest asks for clipboard
	// text (right/middle click). Both run on the UI goroutine.
	OnCopy         func(text string)
	OnPasteRequest func() string
	// OnExit runs (on a background goroutine) when the program ends.
	OnExit func()
}

const (
	mouseX10 = 1 << iota
	mouseNormal
	mouseButtonEvent
	mouseAnyEvent
)

var mouseModeBits = map[ansi.Mode]int32{
	ansi.ModeMouseX10:         mouseX10,
	ansi.ModeMouseNormal:      mouseNormal,
	ansi.ModeMouseButtonEvent: mouseButtonEvent,
	ansi.ModeMouseAnyEvent:    mouseAnyEvent,
}

// NewPane creates an empty pane. redraw is called from background
// goroutines when the screen changed; it must not block.
func NewPane(label string, redraw func()) *Pane {
	p := &Pane{Box: tview.NewBox(), label: label, cols: 80, rows: 24, redraw: redraw}
	p.emu = vt.NewSafeEmulator(p.cols, p.rows)
	p.emu.SetScrollbackSize(scrollbackLines)
	p.title.Store("")
	// Callbacks run inside emulator writes (lock held): no emulator calls.
	p.emu.SetCallbacks(vt.Callbacks{
		Title: func(s string) { p.title.Store(s) },
		EnableMode: func(m ansi.Mode) {
			if m == ansi.ModeCursorKeys {
				p.appCursor.Store(true)
			}
			if bit, ok := mouseModeBits[m]; ok {
				p.mouseModes.Store(p.mouseModes.Load() | bit)
			}
		},
		DisableMode: func(m ansi.Mode) {
			if m == ansi.ModeCursorKeys {
				p.appCursor.Store(false)
			}
			if bit, ok := mouseModeBits[m]; ok {
				p.mouseModes.Store(p.mouseModes.Load() &^ bit)
			}
		},
	})
	// Replies (cursor reports), encoded mouse events and pastes come out
	// of the emulator and go to the program.
	p.inCond = sync.NewCond(&p.inMu)
	go p.writeInput()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := p.emu.Read(buf)
			if n > 0 {
				p.Send(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	p.SetBorder(true)
	p.updateTitle()
	return p
}

func (p *Pane) Label() string { return p.label }

func (p *Pane) SetLabel(l string) {
	p.label = l
	p.updateTitle()
}

func (p *Pane) updateTitle() {
	t := " " + p.label + " "
	if title, _ := p.title.Load().(string); title != "" && title != p.label {
		t = " " + p.label + " — " + title + " "
	}
	if p.scroll > 0 {
		t += "[ ↑ " + itoa(p.scroll) + " lines · scroll down to return ] "
	}
	p.SetTitle(t)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

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
	o.p.scr.Lock()
	n, err := o.p.emu.Write(b)
	o.p.scr.Unlock()
	o.p.scheduleRedraw()
	return n, err
}

// Message writes text straight into the screen (status lines before and
// after a program runs).
func (p *Pane) Message(s string) {
	p.scr.Lock()
	_, _ = p.emu.Write([]byte(s))
	p.scr.Unlock()
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
	p.Message("\r\n\x1b[38;5;245m[session ended — press Enter or click ✕ Close to close this pane]\x1b[0m")
	if p.OnExit != nil {
		p.OnExit()
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
	p.inMu.Lock()
	p.inClosed = true
	p.inCond.Broadcast()
	p.inMu.Unlock()
	if be != nil {
		_ = be.Close()
	}
}

// Send queues raw bytes for the program.
func (p *Pane) Send(b []byte) {
	p.inMu.Lock()
	defer p.inMu.Unlock()
	if p.inClosed {
		return
	}
	p.inQ = append(p.inQ, append([]byte(nil), b...))
	p.inCond.Signal()
}

// writeInput writes queued input to the program, in order.
func (p *Pane) writeInput() {
	for {
		p.inMu.Lock()
		for len(p.inQ) == 0 && !p.inClosed {
			p.inCond.Wait()
		}
		if p.inClosed {
			p.inMu.Unlock()
			return
		}
		q := p.inQ
		p.inQ = nil
		p.inMu.Unlock()

		p.mu.Lock()
		be := p.be
		p.mu.Unlock()
		if be == nil {
			continue
		}
		for _, b := range q {
			if _, err := be.Write(b); err != nil {
				break
			}
		}
	}
}

// Type sends text as if typed (used for saved commands).
func (p *Pane) Type(text string) {
	p.scrollToBottom()
	p.Send([]byte(text))
}

// Paste sends text as a paste (bracketed when the program asked for it).
func (p *Pane) Paste(text string) {
	p.scrollToBottom()
	p.scr.Lock()
	p.emu.Paste(text)
	p.scr.Unlock()
}

func (p *Pane) scrollToBottom() {
	p.scroll = 0
	p.hasSel = false
}

// ---------------------------------------------------------------------------
// Content access
// ---------------------------------------------------------------------------

// cellAt returns the cell on absolute line `line` (see Pane.scroll).
func (p *Pane) cellAt(x, line, sbLen int) *uv.Cell {
	if line < sbLen {
		return p.emu.ScrollbackCellAt(x, line)
	}
	return p.emu.CellAt(x, line-sbLen)
}

// lineText needs p.scr held.
func (p *Pane) lineText(line, sbLen, from, to int) string {
	var b strings.Builder
	for x := from; x < to; x++ {
		c := p.cellAt(x, line, sbLen)
		switch {
		case c == nil:
			b.WriteByte(' ')
		case c.Content == "":
			if c.Width == 0 {
				continue // second half of a wide character
			}
			b.WriteByte(' ')
		default:
			b.WriteString(c.Content)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// CursorLine returns the text of the cursor's line up to the cursor (what
// is being typed at a shell prompt, prompt included).
func (p *Pane) CursorLine() string {
	p.scr.RLock()
	defer p.scr.RUnlock()
	pos := p.emu.CursorPosition()
	return p.lineText(pos.Y, 0, 0, p.emu.Width())
}

// ScreenText returns the visible screen (for tests and debugging).
func (p *Pane) ScreenText() string {
	p.scr.RLock()
	defer p.scr.RUnlock()
	var lines []string
	for y := 0; y < p.emu.Height(); y++ {
		lines = append(lines, p.lineText(y, 0, 0, p.emu.Width()))
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Drawing
// ---------------------------------------------------------------------------

func (p *Pane) Draw(screen tcell.Screen) {
	p.pending.Store(false)
	x, y, w, h := p.GetInnerRect()
	p.mu.Lock()
	resized := w > 0 && h > 0 && (w != p.cols || h != p.rows)
	if resized {
		p.cols, p.rows = w, h
	}
	be := p.be
	p.mu.Unlock()
	if resized {
		p.scr.Lock()
		p.emu.Resize(w, h)
		p.scr.Unlock()
		if be != nil {
			_ = be.Resize(w, h)
		}
	}

	p.scr.RLock()
	defer p.scr.RUnlock()
	sbLen := p.emu.ScrollbackLen()
	if p.emu.IsAltScreen() {
		p.scroll = 0
	} else if p.scroll > 0 && sbLen > p.lastSB {
		p.scroll += sbLen - p.lastSB // keep the viewport on the same lines
	}
	p.lastSB = sbLen
	p.scroll = min(p.scroll, sbLen)

	p.updateTitle()
	p.Box.DrawForSubclass(screen, p)
	if w <= 0 || h <= 0 {
		return
	}
	bg := p.GetBackgroundColor()
	top := sbLen - p.scroll
	for row := 0; row < h; row++ {
		line := top + row
		for col := 0; col < w; col++ {
			c := p.cellAt(col, line, sbLen)
			ch, comb, st := ' ', []rune(nil), tcell.StyleDefault.Background(bg)
			if c != nil {
				st = cellStyle(c, bg)
				if c.Content != "" {
					rs := []rune(c.Content)
					ch, comb = rs[0], rs[1:]
				} else if c.Width == 0 {
					continue // covered by the wide character before it
				}
			}
			if p.selected(col, line) {
				st = st.Reverse(true)
			}
			screen.SetContent(x+col, y+row, ch, comb, st)
		}
	}
	if p.HasFocus() && p.scroll == 0 && !p.emu.CursorHidden() {
		pos := p.emu.CursorPosition()
		if pos.X < w && pos.Y < h {
			screen.ShowCursor(x+pos.X, y+pos.Y)
		}
	}
}

func cellStyle(c *uv.Cell, bg tcell.Color) tcell.Style {
	s := c.Style
	st := tcell.StyleDefault.Foreground(toColor(s.Fg, tcell.ColorDefault)).Background(toColor(s.Bg, bg))
	a := s.Attrs
	st = st.Bold(a&uv.AttrBold != 0).Dim(a&uv.AttrFaint != 0).Italic(a&uv.AttrItalic != 0).
		Blink(a&uv.AttrBlink != 0).Reverse(a&uv.AttrReverse != 0).StrikeThrough(a&uv.AttrStrikethrough != 0)
	if s.Underline != uv.UnderlineNone {
		st = st.Underline(true)
	}
	return st
}

func toColor(c color.Color, def tcell.Color) tcell.Color {
	switch c := c.(type) {
	case nil:
		return def
	case ansi.BasicColor:
		return tcell.PaletteColor(int(c))
	case ansi.IndexedColor:
		return tcell.PaletteColor(int(c))
	default:
		r, g, b, _ := c.RGBA()
		return tcell.NewRGBColor(int32(r>>8), int32(g>>8), int32(b>>8))
	}
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

func selOrder(a, b [2]int) ([2]int, [2]int) {
	if a[1] > b[1] || (a[1] == b[1] && a[0] > b[0]) {
		return b, a
	}
	return a, b
}

func (p *Pane) selected(x, line int) bool {
	if !p.hasSel {
		return false
	}
	s, e := selOrder(p.selA, p.selB)
	if line < s[1] || line > e[1] {
		return false
	}
	if line == s[1] && x < s[0] {
		return false
	}
	if line == e[1] && x > e[0] {
		return false
	}
	return true
}

func (p *Pane) selectionText() string {
	p.scr.RLock()
	defer p.scr.RUnlock()
	sbLen := p.emu.ScrollbackLen()
	s, e := selOrder(p.selA, p.selB)
	w := p.emu.Width()
	var lines []string
	for line := s[1]; line <= e[1]; line++ {
		from, to := 0, w
		if line == s[1] {
			from = s[0]
		}
		if line == e[1] {
			to = e[0] + 1
		}
		lines = append(lines, p.lineText(line, sbLen, from, min(to, w)))
	}
	return strings.Join(lines, "\n")
}

// selectWord selects the word under (x, line).
func (p *Pane) selectWord(x, line int) {
	p.scr.RLock()
	defer p.scr.RUnlock()
	sbLen := p.emu.ScrollbackLen()
	isWord := func(x int) bool {
		c := p.cellAt(x, line, sbLen)
		if c == nil || c.Content == "" {
			return false
		}
		r := []rune(c.Content)[0]
		return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./~:@%+=", r)
	}
	if !isWord(x) {
		return
	}
	from, to := x, x
	for from > 0 && isWord(from-1) {
		from--
	}
	for to < p.emu.Width()-1 && isWord(to+1) {
		to++
	}
	p.selA, p.selB, p.hasSel = [2]int{from, line}, [2]int{to, line}, true
}

func (p *Pane) copySelection() {
	if !p.hasSel || p.OnCopy == nil {
		return
	}
	if text := p.selectionText(); text != "" {
		p.OnCopy(text)
	}
}

// ---------------------------------------------------------------------------
// Input
// ---------------------------------------------------------------------------

func (p *Pane) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return p.WrapInputHandler(func(ev *tcell.EventKey, _ func(tview.Primitive)) {
		switch {
		// Shift+PgUp/PgDn scroll the history like most terminals.
		case ev.Key() == tcell.KeyPgUp && ev.Modifiers()&tcell.ModShift != 0:
			p.scrollBy(p.rows - 1)
			return
		case ev.Key() == tcell.KeyPgDn && ev.Modifiers()&tcell.ModShift != 0:
			p.scrollBy(-(p.rows - 1))
			return
		}
		p.scrollToBottom()
		p.Send(keyBytes(ev, p.appCursor.Load()))
	})
}

func (p *Pane) scrollBy(n int) {
	if p.altScreen() {
		return
	}
	p.scroll = max(0, min(p.scroll+n, p.sbLen()))
}

func (p *Pane) PasteHandler() func(string, func(tview.Primitive)) {
	return p.WrapPasteHandler(func(text string, _ func(tview.Primitive)) {
		p.Paste(text)
	})
}

func (p *Pane) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return p.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		mx, my := ev.Position()
		if !p.InRect(mx, my) && !p.selecting && !p.fwdHeld {
			return false, nil
		}
		ix, iy, w, h := p.GetInnerRect()
		col := max(0, min(mx-ix, w-1))
		row := max(0, min(my-iy, h-1))
		line := p.sbLen() - p.scroll + row
		if action == tview.MouseLeftDown || action == tview.MouseRightDown || action == tview.MouseMiddleDown {
			setFocus(p)
		}

		// The program asked for mouse events (htop, vim, mc…): forward
		// them, unless Shift is held, which selects text like xterm does.
		// Only events inside the program's area are forwarded, and the
		// mouse is held only while a button pressed there is down, so the
		// border, title buttons and everything around stay clickable.
		inside := mx >= ix && mx < ix+w && my >= iy && my < iy+h
		if modes := p.mouseModes.Load(); modes != 0 && ev.Modifiers()&tcell.ModShift == 0 && p.scroll == 0 && (inside || p.fwdHeld) {
			if p.sendMouse(action, ev, col, row, modes) {
				switch action {
				case tview.MouseLeftDown, tview.MouseMiddleDown, tview.MouseRightDown:
					p.fwdHeld = true
				case tview.MouseLeftUp, tview.MouseMiddleUp, tview.MouseRightUp:
					p.fwdHeld = false
				}
				if p.fwdHeld {
					return true, p
				}
				return true, nil
			}
		}
		if !inside && action == tview.MouseLeftDown {
			return true, nil // a click on the border is not a selection
		}

		switch action {
		case tview.MouseScrollUp:
			if p.altScreen() {
				p.sendArrows(true) // e.g. less, man: scroll their own view
			} else {
				p.scrollBy(wheelLines)
			}
			return true, nil
		case tview.MouseScrollDown:
			if p.altScreen() {
				p.sendArrows(false)
			} else {
				p.scrollBy(-wheelLines)
			}
			return true, nil
		case tview.MouseLeftDown:
			p.selecting = true
			p.hasSel = false
			p.selA, p.selB = [2]int{col, line}, [2]int{col, line}
			return true, p // capture the mouse while dragging
		case tview.MouseMove:
			if p.selecting {
				// Dragging past the top or bottom edge scrolls.
				if my < iy {
					p.scrollBy(1)
				} else if my >= iy+h {
					p.scrollBy(-1)
				}
				line = p.sbLen() - p.scroll + row
				p.selB = [2]int{col, line}
				p.hasSel = p.selA != p.selB
				return true, p
			}
		case tview.MouseLeftUp:
			if p.selecting {
				p.selecting = false
				p.copySelection()
				return true, nil
			}
		case tview.MouseLeftDoubleClick:
			p.selectWord(col, line)
			p.copySelection()
			return true, nil
		case tview.MouseRightClick, tview.MouseMiddleClick:
			if p.OnPasteRequest != nil {
				if text := p.OnPasteRequest(); text != "" {
					p.Paste(text)
				}
			}
			return true, nil
		case tview.MouseLeftClick:
			return true, nil
		}
		return p.InRect(mx, my), nil
	})
}

func (p *Pane) sendArrows(up bool) {
	key := tcell.KeyDown
	if up {
		key = tcell.KeyUp
	}
	b := keyBytes(tcell.NewEventKey(key, 0, tcell.ModNone), p.appCursor.Load())
	for range wheelLines {
		p.Send(b)
	}
}

// sendMouse forwards a mouse event to a program that enabled mouse
// tracking. It reports whether the event was consumed.
func (p *Pane) sendMouse(action tview.MouseAction, ev *tcell.EventMouse, col, row int, modes int32) bool {
	m := uv.Mouse{X: col, Y: row}
	if ev.Modifiers()&tcell.ModCtrl != 0 {
		m.Mod |= uv.ModCtrl
	}
	if ev.Modifiers()&tcell.ModAlt != 0 {
		m.Mod |= uv.ModAlt
	}
	switch action {
	case tview.MouseLeftDown:
		m.Button = uv.MouseLeft
		p.mouse(uv.MouseClickEvent(m))
	case tview.MouseMiddleDown:
		m.Button = uv.MouseMiddle
		p.mouse(uv.MouseClickEvent(m))
	case tview.MouseRightDown:
		m.Button = uv.MouseRight
		p.mouse(uv.MouseClickEvent(m))
	case tview.MouseLeftUp:
		m.Button = uv.MouseLeft
		p.mouse(uv.MouseReleaseEvent(m))
	case tview.MouseMiddleUp:
		m.Button = uv.MouseMiddle
		p.mouse(uv.MouseReleaseEvent(m))
	case tview.MouseRightUp:
		m.Button = uv.MouseRight
		p.mouse(uv.MouseReleaseEvent(m))
	case tview.MouseScrollUp:
		m.Button = uv.MouseWheelUp
		p.mouse(uv.MouseWheelEvent(m))
	case tview.MouseScrollDown:
		m.Button = uv.MouseWheelDown
		p.mouse(uv.MouseWheelEvent(m))
	case tview.MouseMove:
		if modes&(mouseButtonEvent|mouseAnyEvent) == 0 {
			return true
		}
		btn := ev.Buttons()
		switch {
		case btn&tcell.Button1 != 0:
			m.Button = uv.MouseLeft
		case btn&tcell.Button3 != 0:
			m.Button = uv.MouseMiddle
		case btn&tcell.Button2 != 0:
			m.Button = uv.MouseRight
		case modes&mouseAnyEvent == 0:
			return true // plain motion only in any-event mode
		}
		p.mouse(uv.MouseMotionEvent(m))
	case tview.MouseLeftClick, tview.MouseMiddleClick, tview.MouseRightClick, tview.MouseLeftDoubleClick:
		// Already sent as down/up.
	default:
		return false
	}
	return true
}

// Locked emulator access for code outside Draw and the text helpers.
func (p *Pane) sbLen() int {
	p.scr.RLock()
	defer p.scr.RUnlock()
	return p.emu.ScrollbackLen()
}

func (p *Pane) altScreen() bool {
	p.scr.RLock()
	defer p.scr.RUnlock()
	return p.emu.IsAltScreen()
}

func (p *Pane) mouse(ev uv.MouseEvent) {
	p.scr.Lock()
	defer p.scr.Unlock()
	p.emu.SendMouse(ev)
}
