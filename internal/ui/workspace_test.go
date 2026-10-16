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

	// Split right with a second local shell.
	sim.InjectKey(tcell.KeyCtrlB, 0, tcell.ModNone)
	typeText(sim, "|")
	waitFor(t, app, sim, "Open in new pane")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // "Local shell"
	time.Sleep(300 * time.Millisecond)
	if n := paneCount(app); n != 2 {
		t.Fatalf("panes = %d, want 2", n)
	}

	// Ctrl+B 1 types the first saved command (without Enter).
	sim.InjectKey(tcell.KeyCtrlB, 0, tcell.ModNone)
	typeText(sim, "1")
	waitFor(t, app, sim, "$ echo saved-cmd-ran")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "║saved-cmd-ran")

	// Back to the list and in again: sessions are still there.
	sim.InjectKey(tcell.KeyCtrlB, 0, tcell.ModNone)
	typeText(sim, "w")
	waitFor(t, app, sim, "Servers")
	typeText(sim, "w")
	waitFor(t, app, sim, "pane-one-42")

	// Exiting a shell then Enter closes its pane.
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
