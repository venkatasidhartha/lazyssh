package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func commandCount(a *App) (n int) {
	a.app.QueueUpdate(func() {
		cmds, _ := a.store.Commands()
		n = len(cmds)
	})
	return n
}

// The ★ Commands window: add, edit, type, run and remove saved commands,
// including removing the last one (which used to crash on the next Enter).
func TestCommandManager(t *testing.T) {
	app, sim, done := startApp(t)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ")

	clickText(t, app, sim, "★ Commands")
	waitFor(t, app, sim, "No saved commands yet")
	waitFor(t, app, sim, "$ ") // the sessions stay visible behind it

	// + New: command and name.
	// The bar behind the window is blocked while it is open.
	clickText(t, app, sim, "+ Add")
	waitFor(t, app, sim, "No saved commands yet")
	clickLastText(t, app, sim, "+ New ")
	waitFor(t, app, sim, "New command")
	typeText(sim, "echo first-$((2+3))")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, "five")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // Save
	waitFor(t, app, sim, "Saved commands")
	waitFor(t, app, sim, "1 five")
	if n := commandCount(app); n != 1 {
		t.Fatalf("commands = %d", n)
	}

	// ✎ Edit changes it.
	clickLastText(t, app, sim, "✎ Edit")
	waitFor(t, app, sim, "Edit command")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyCtrlU, 0, tcell.ModCtrl)
	typeText(sim, "add")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "1 add")

	// ↵ Run types it and presses Enter in the session.
	clickLastText(t, app, sim, "↵ Run")
	waitForGone(t, app, sim, "Saved commands")
	waitFor(t, app, sim, "║first-5") // output, not just the typed line

	// The bar chip shows the new name and ▶ Type types without Enter.
	waitFor(t, app, sim, "1 add")
	clickText(t, app, sim, "★ Commands")
	waitFor(t, app, sim, "Saved commands")
	clickLastText(t, app, sim, "▶ Type")
	waitFor(t, app, sim, "$ echo first-$((2+3))")
	sim.InjectKey(tcell.KeyCtrlU, 0, tcell.ModCtrl)

	// ✕ Remove asks first, then removes. Removing the last one and
	// pressing Enter must not crash.
	clickText(t, app, sim, "★ Commands")
	waitFor(t, app, sim, "Saved commands")
	clickLastText(t, app, sim, "✕ Remove")
	waitFor(t, app, sim, "Yes, remove")
	clickLastText(t, app, sim, "✕ Yes, remove")
	waitFor(t, app, sim, "No saved commands yet")
	if n := commandCount(app); n != 0 {
		t.Fatalf("commands after remove = %d", n)
	}
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyDelete, 0, tcell.ModNone)
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("app ended: %v", err)
	default:
	}
	sim.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)
	waitForGone(t, app, sim, "Saved commands")
}

// Ctrl+C never closes lazyssh; Ctrl+Shift+C copies the selection.
func TestCtrlCNeverQuits(t *testing.T) {
	app, sim, done := startApp(t)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ")
	alive := func(where string) {
		t.Helper()
		time.Sleep(200 * time.Millisecond)
		select {
		case <-done:
			t.Fatalf("Ctrl+C in %s closed lazyssh", where)
		default:
		}
	}

	// In a dialog, in the server manager, and Ctrl+Shift+C in a session
	// sent the way kitty/xterm keyboard modes send it.
	clickText(t, app, sim, "? Help")
	waitFor(t, app, sim, "Esc closes this help")
	sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModCtrl)
	alive("a dialog")
	sim.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)

	clickText(t, app, sim, "☰ Servers")
	waitFor(t, app, sim, "No servers yet")
	sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModCtrl)
	alive("the server list")
	waitFor(t, app, sim, "press q to quit")
	typeText(sim, "w")
	waitFor(t, app, sim, "$ ")

	var copied string
	copyToClipboard = func(s string) { copied = s }
	typeText(sim, "echo copy-me-now")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "║copy-me-now")
	x, y := findText(t, app, sim, "║copy-me-now")
	sim.InjectMouse(x+1, y, tcell.Button1, tcell.ModNone)
	sim.InjectMouse(x+11, y, tcell.Button1, tcell.ModNone)
	sim.InjectMouse(x+11, y, tcell.ButtonNone, tcell.ModNone)
	waitFor(t, app, sim, "Copied 11 characters")
	app.app.QueueUpdate(func() { copied = "" })
	sim.InjectKey(tcell.KeyRune, 'C', tcell.ModCtrl|tcell.ModShift)
	alive("a session (Ctrl+Shift+C)")
	waitFor(t, app, sim, "Copied 11 characters")
	var got string
	app.app.QueueUpdate(func() { got = copied })
	if got != "copy-me-now" {
		t.Fatalf("Ctrl+Shift+C copied %q", got)
	}
}

// clickLastText clicks the last occurrence of text (dialogs are drawn
// below the bars, so their buttons come last).
func clickLastText(t *testing.T, a *App, s tcell.SimulationScreen, text string) {
	t.Helper()
	x, y := -1, -1
	for i, line := range strings.Split(readScreen(a, s), "\n") {
		if j := strings.LastIndex(line, text); j >= 0 {
			x, y = len([]rune(line[:j])), i
		}
	}
	if x < 0 {
		t.Fatalf("%q not on screen", text)
	}
	s.InjectMouse(x+1, y, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	s.InjectMouse(x+1, y, tcell.ButtonNone, tcell.ModNone)
	time.Sleep(100 * time.Millisecond)
}
