package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

// screenText returns the simulated screen as lines.
func screenText(s tcell.SimulationScreen) string {
	cells, w, h := s.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := cells[y*w+x].Runes
			if len(r) == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteRune(r[0])
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// waitFor polls the screen (read on the UI goroutine) until it shows want.
func waitFor(t *testing.T, a *App, s tcell.SimulationScreen, want string) {
	t.Helper()
	var text string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.app.QueueUpdate(func() { text = screenText(s) })
		if strings.Contains(text, want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q:\n%s", want, text)
}

func paneCount(a *App) (n int) {
	a.app.QueueUpdate(func() { n = a.workspace.PaneCount() })
	return n
}

func typeText(s tcell.SimulationScreen, text string) {
	for _, r := range text {
		s.InjectKey(tcell.KeyRune, r, tcell.ModNone)
		time.Sleep(2 * time.Millisecond)
	}
}

func TestWorkspaceLocalPanes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := sshx.LoadOrCreateManagedKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddCommand("greet", "echo saved-cmd-ran"); err != nil {
		t.Fatal(err)
	}

	sim := tcell.NewSimulationScreen("UTF-8")
	app := New(st, &sshx.Connector{Key: key}, "test")
	app.app.SetScreen(sim)
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	sim.SetSize(140, 40)

	waitFor(t, app, sim, "Workspace")
	// No servers: 'w' opens a local shell.
	typeText(sim, "w")
	waitFor(t, app, sim, "greet") // commands bar
	waitFor(t, app, sim, "$ ")
	typeText(sim, "echo pane-one-$((40+2))")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "pane-one-42")

	// Ctrl+C goes to the shell, not to tview (which would quit).
	sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModNone)
	time.Sleep(200 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Ctrl+C quit the app")
	default:
	}

	// Scrollback: the mouse wheel scrolls back to output that went off screen.
	typeText(sim, "echo TOP-MARK; seq 1 200")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "200 ")
	if strings.Contains(readScreen(app, sim), "TOP-MARK\n") {
		t.Fatal("TOP-MARK still on screen before scrolling")
	}
	x, y := findText(t, app, sim, "200 ")
	for range 80 {
		sim.InjectMouse(x, y, tcell.WheelUp, tcell.ModNone)
	}
	waitFor(t, app, sim, "lines · scroll down to return")
	waitFor(t, app, sim, "TOP-MARK ")
	// Typing jumps back to the bottom.
	typeText(sim, "clear")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitForGone(t, app, sim, "scroll down to return")

	// Split right by clicking the toolbar button.
	clickText(t, app, sim, "⇆ Split right")
	waitFor(t, app, sim, "New session — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // "Local shell"
	time.Sleep(300 * time.Millisecond)
	if n := paneCount(app); n != 2 {
		t.Fatalf("panes = %d, want 2", n)
	}
	waitFor(t, app, sim, "2 local")

	// Clicking a saved command chip types it (without Enter).
	clickText(t, app, sim, "1 greet")
	waitFor(t, app, sim, "$ echo saved-cmd-ran")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "║saved-cmd-ran")

	// Ctrl+A then a single key: CONTROL mode, 1 types saved command 1.
	sim.InjectKey(tcell.KeyCtrlA, 0, tcell.ModCtrl)
	waitFor(t, app, sim, "CONTROL")
	typeText(sim, "1")
	waitFor(t, app, sim, "TERMINAL")
	waitFor(t, app, sim, "$ echo saved-cmd-ran ")

	// Drag over text in the first pane copies it.
	var copied string
	copyToClipboard = func(s string) { copied = s }
	sim.InjectKey(tcell.KeyCtrlU, 0, tcell.ModCtrl) // clear the typed line
	typeText(sim, "echo drag-me-please")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "║drag-me-please")
	x, y = findText(t, app, sim, "║drag-me-please")
	x++
	sim.InjectMouse(x, y, tcell.Button1, tcell.ModNone)
	sim.InjectMouse(x+6, y, tcell.Button1, tcell.ModNone)
	sim.InjectMouse(x+13, y, tcell.Button1, tcell.ModNone)
	sim.InjectMouse(x+13, y, tcell.ButtonNone, tcell.ModNone)
	waitFor(t, app, sim, "Copied 14 characters")
	var got string
	app.app.QueueUpdate(func() { got = copied })
	if got != "drag-me-please" {
		t.Fatalf("copied %q", got)
	}

	// Back to the server list and in again: sessions are still there.
	clickText(t, app, sim, "☰ Server list")
	waitFor(t, app, sim, "Servers")
	typeText(sim, "w")
	waitFor(t, app, sim, "drag-me-please")

	// Exiting a shell, then Enter, closes its pane.
	typeText(sim, "exit")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "session ended")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	time.Sleep(200 * time.Millisecond)
	if n := paneCount(app); n != 1 {
		t.Fatalf("panes after close = %d, want 1", n)
	}

	app.app.Stop()
	<-done
}

func readScreen(a *App, s tcell.SimulationScreen) (text string) {
	a.app.QueueUpdate(func() { text = screenText(s) })
	return text
}

func waitForGone(t *testing.T, a *App, s tcell.SimulationScreen, text string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !strings.Contains(readScreen(a, s), text) {
			return
		}
	}
	t.Fatalf("screen still shows %q", text)
}

// findText returns the screen position of the first occurrence of text.
func findText(t *testing.T, a *App, s tcell.SimulationScreen, text string) (int, int) {
	t.Helper()
	for y, line := range strings.Split(readScreen(a, s), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return len([]rune(line[:i])), y
		}
	}
	t.Fatalf("%q not on screen", text)
	return 0, 0
}

func clickText(t *testing.T, a *App, s tcell.SimulationScreen, text string) {
	t.Helper()
	x, y := findText(t, a, s, text)
	s.InjectMouse(x+1, y, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	s.InjectMouse(x+1, y, tcell.ButtonNone, tcell.ModNone)
	time.Sleep(100 * time.Millisecond)
}
