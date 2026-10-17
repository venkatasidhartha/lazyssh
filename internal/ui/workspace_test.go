package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"lazyssh/internal/domain"
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

	// No servers: lazyssh starts in a local terminal.
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
	waitFor(t, app, sim, "Split right — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // "Local shell"
	time.Sleep(300 * time.Millisecond)
	if n := paneCount(app); n != 2 {
		t.Fatalf("panes = %d, want 2", n)
	}
	waitFor(t, app, sim, "2 local")

	// Dragging the border between the two panes resizes them.
	var lw, rx, ry int
	app.app.QueueUpdate(func() {
		l := app.workspace.leaves()
		_, _, lw, _ = l[0].pane.GetRect()
		rx, ry, _, _ = l[1].pane.GetRect()
	})
	sim.InjectMouse(rx, ry+5, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	sim.InjectMouse(rx-20, ry+5, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	sim.InjectMouse(rx-20, ry+5, tcell.ButtonNone, tcell.ModNone)
	time.Sleep(200 * time.Millisecond)
	var lw2 int
	app.app.QueueUpdate(func() { _, _, lw2, _ = app.workspace.leaves()[0].pane.GetRect() })
	if lw2 > lw-15 || lw2 < lw-25 {
		t.Fatalf("left pane width %d -> %d after dragging the border 20 columns", lw, lw2)
	}

	// The ⇅ button in the pane's own title bar splits that pane.
	clickPaneButton(t, app, sim, "splith")
	waitFor(t, app, sim, "Split down — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	time.Sleep(300 * time.Millisecond)
	if n := paneCount(app); n != 3 {
		t.Fatalf("panes = %d, want 3", n)
	}
	// ✎ names it; the name shows in its title and in the session list.
	clickPaneButton(t, app, sim, "rename")
	waitFor(t, app, sim, "Rename session")
	sim.InjectKey(tcell.KeyCtrlU, 0, tcell.ModCtrl)
	typeText(sim, "app logs")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "3 app logs")
	waitFor(t, app, sim, "╔ app logs ")

	// ⤢ fills the whole terminal; ⤡ goes back to the panes.
	clickPaneButton(t, app, sim, "zoom")
	waitFor(t, app, sim, "⤢ app logs")
	waitForGone(t, app, sim, "Sessions")
	typeText(sim, "echo zoomed-$((3*3))")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "zoomed-9")
	clickText(t, app, sim, "⤡ Exit full screen")
	waitFor(t, app, sim, "Sessions")
	// Double-clicking a title bar does the same.
	var tx, ty int
	app.app.QueueUpdate(func() { tx, ty, _, _ = app.workspace.focused.pane.GetRect() })
	for range 2 {
		sim.InjectMouse(tx+3, ty, tcell.Button1, tcell.ModNone)
		time.Sleep(10 * time.Millisecond)
		sim.InjectMouse(tx+3, ty, tcell.ButtonNone, tcell.ModNone)
		time.Sleep(10 * time.Millisecond)
	}
	waitFor(t, app, sim, "⤡ Exit full screen")
	clickText(t, app, sim, "⤡ Exit full screen")
	waitFor(t, app, sim, "Sessions")

	// ⊟ Even sizes undoes the border drag.
	clickText(t, app, sim, "⊟ Even sizes")
	var w0, w1 int
	app.app.QueueUpdate(func() {
		l := app.workspace.leaves()
		_, _, w0, _ = l[0].pane.GetRect()
		_, _, w1, _ = l[1].prim.GetRect()
	})
	if d := w0 - w1; d < -1 || d > 1 {
		t.Fatalf("even sizes: %d vs %d", w0, w1)
	}

	clickPaneButton(t, app, sim, "close")
	time.Sleep(200 * time.Millisecond)
	if n := paneCount(app); n != 2 {
		t.Fatalf("panes after ✕ = %d, want 2", n)
	}
	app.app.QueueUpdate(func() { app.workspace.focus(app.workspace.leaves()[1]) })

	// Clicking a saved command chip types it (without Enter).
	clickText(t, app, sim, "1 greet")
	waitFor(t, app, sim, "$ echo saved-cmd-ran")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "║saved-cmd-ran")

	// Ctrl+A then a single key: CONTROL mode, 1 types saved command 1.
	sim.InjectKey(tcell.KeyCtrlA, 0, tcell.ModCtrl)
	waitFor(t, app, sim, "CONTROL")
	typeText(sim, "1")
	waitFor(t, app, sim, "click a pane to type in it")
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
	waitFor(t, app, sim, "No servers yet")
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

// clickPaneButton clicks a title-bar button of the focused pane.
func clickPaneButton(t *testing.T, a *App, s tcell.SimulationScreen, id string) {
	t.Helper()
	x, y := -1, -1
	a.app.QueueUpdate(func() {
		for _, h := range a.workspace.panes.hits {
			if h.id == id && h.node == a.workspace.focused {
				x, y = h.x0+1, h.y
			}
		}
	})
	if x < 0 {
		t.Fatalf("no %s button on the focused pane:\n%s", id, readScreen(a, s))
	}
	s.InjectMouse(x, y, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	s.InjectMouse(x, y, tcell.ButtonNone, tcell.ModNone)
	time.Sleep(150 * time.Millisecond)
}

func clickText(t *testing.T, a *App, s tcell.SimulationScreen, text string) {
	t.Helper()
	x, y := findText(t, a, s, text)
	s.InjectMouse(x+1, y, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	s.InjectMouse(x+1, y, tcell.ButtonNone, tcell.ModNone)
	time.Sleep(100 * time.Millisecond)
}

func startApp(t *testing.T, servers ...domain.Server) (*App, tcell.SimulationScreen, chan error) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		if _, err := st.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	key, err := sshx.LoadOrCreateManagedKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	sim := tcell.NewSimulationScreen("UTF-8")
	app := New(st, &sshx.Connector{Key: key}, "test")
	app.app.SetScreen(sim)
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	sim.SetSize(140, 40)
	return app, sim, done
}

// Without any server the terminal features work on their own, and closing
// the last session leaves you in the workspace, not in an empty manager.
func TestWorkspaceWithoutServers(t *testing.T) {
	app, sim, done := startApp(t)
	waitFor(t, app, sim, "$ ")
	waitFor(t, app, sim, "▶ Local terminal")
	waitFor(t, app, sim, "+ Add server")

	typeText(sim, "exit")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "session ended")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "No session open")
	clickText(t, app, sim, "▶ Open a local terminal")
	waitFor(t, app, sim, "$ ")
	if n := paneCount(app); n != 1 {
		t.Fatalf("panes = %d", n)
	}

	// + Add server opens the form; cancelling comes back to the workspace.
	clickText(t, app, sim, "+ Add server")
	waitFor(t, app, sim, "Alias")
	sim.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)
	waitFor(t, app, sim, "▶ Local terminal")
	waitFor(t, app, sim, "$ ")

	app.app.Stop()
	<-done
}

// With saved servers lazyssh starts in the manager; ▶ Terminal opens a
// local shell and the servers are one click away in the session list.
func TestTerminalButtonWithServers(t *testing.T) {
	app, sim, done := startApp(t, domain.Server{Alias: "web-1", Host: "10.0.0.9", User: "deploy"})
	waitFor(t, app, sim, "web-1")
	var inWS bool
	app.app.QueueUpdate(func() { inWS = app.inWorkspace })
	if inWS {
		t.Fatal("started in the workspace although servers exist")
	}
	clickText(t, app, sim, "▶ Terminal")
	waitFor(t, app, sim, "$ ")
	waitFor(t, app, sim, "⇢ web-1")
	if n := paneCount(app); n != 1 {
		t.Fatalf("panes = %d", n)
	}
	app.app.Stop()
	<-done
}

func TestPromptGuess(t *testing.T) {
	for line, want := range map[string]string{
		"$":                           "",
		"user@web:~$":                 "",
		"user@web:~$ tail -f app.log": "tail -f app.log",
		"root@db:/# systemctl status": "systemctl status",
		"❯ make test":                 "make test",
		"":                            "",
	} {
		if got := promptGuess(line); got != want {
			t.Errorf("promptGuess(%q) = %q, want %q", line, got, want)
		}
	}
}
