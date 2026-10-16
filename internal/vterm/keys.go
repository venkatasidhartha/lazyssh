package vterm

import (
	"fmt"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

var fnKeys = map[tcell.Key]string{
	tcell.KeyF1: "\x1bOP", tcell.KeyF2: "\x1bOQ", tcell.KeyF3: "\x1bOR", tcell.KeyF4: "\x1bOS",
	tcell.KeyF5: "\x1b[15~", tcell.KeyF6: "\x1b[17~", tcell.KeyF7: "\x1b[18~", tcell.KeyF8: "\x1b[19~",
	tcell.KeyF9: "\x1b[20~", tcell.KeyF10: "\x1b[21~", tcell.KeyF11: "\x1b[23~", tcell.KeyF12: "\x1b[24~",
	tcell.KeyInsert: "\x1b[2~", tcell.KeyDelete: "\x1b[3~",
	tcell.KeyPgUp: "\x1b[5~", tcell.KeyPgDn: "\x1b[6~",
}

// cursorKeys use a final letter; with modifiers they become CSI 1;<m><X>.
var cursorKeys = map[tcell.Key]byte{
	tcell.KeyUp: 'A', tcell.KeyDown: 'B', tcell.KeyRight: 'C', tcell.KeyLeft: 'D',
	tcell.KeyHome: 'H', tcell.KeyEnd: 'F',
}

// keyBytes translates a key event into what an xterm would send.
func keyBytes(ev *tcell.EventKey, appCursor bool) []byte {
	mod := ev.Modifiers()
	key := ev.Key()

	if key == tcell.KeyRune {
		buf := make([]byte, 0, 5)
		if mod&tcell.ModAlt != 0 {
			buf = append(buf, 0x1b)
		}
		return utf8.AppendRune(buf, ev.Rune())
	}
	if c, ok := cursorKeys[key]; ok {
		if m := xtermMod(mod); m > 1 {
			return fmt.Appendf(nil, "\x1b[1;%d%c", m, c)
		}
		if appCursor {
			return []byte{0x1b, 'O', c}
		}
		return []byte{0x1b, '[', c}
	}
	if s, ok := fnKeys[key]; ok {
		return []byte(s)
	}
	var b []byte
	switch {
	case key == tcell.KeyBacktab:
		return []byte("\x1b[Z")
	case key == tcell.KeyBackspace:
		// tcell reports both BS and DEL as Backspace; terminals send DEL.
		b = []byte{0x7f}
	case key >= tcell.KeyCtrlA && key <= tcell.KeyCtrlZ:
		b = []byte{CtrlByte(key)}
	case key < 0x20: // Enter, Tab, Esc, Ctrl+\ ] ^ _
		b = []byte{byte(key)}
	default:
		return nil
	}
	if mod&tcell.ModAlt != 0 {
		b = append([]byte{0x1b}, b...)
	}
	return b
}

// CtrlByte is the control character for tcell.KeyCtrlA … KeyCtrlZ.
func CtrlByte(k tcell.Key) byte { return byte(k-tcell.KeyCtrlA) + 1 }

func xtermMod(m tcell.ModMask) int {
	n := 1
	if m&tcell.ModShift != 0 {
		n++
	}
	if m&tcell.ModAlt != 0 {
		n += 2
	}
	if m&tcell.ModCtrl != 0 {
		n += 4
	}
	return n
}
