package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/sessions"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

// runApp starts lazyssh on dir; with service it uses the session service
// running there.
func runApp(t *testing.T, dir string, service bool) (*App, tcell.SimulationScreen, chan error) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := sshx.LoadOrCreateManagedKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	sim := tcell.NewSimulationScreen("UTF-8")
	app := New(st, &sshx.Connector{Key: key}, "test")
	if service {
		app.Sessions = func() (*sessions.Client, error) { return sessions.Dial(dir) }
	}
	app.app.SetScreen(sim)
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	sim.SetSize(140, 40)
	return app, sim, done
}

func startService(t *testing.T, dir string) *sessions.Server {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := sessions.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := sessions.NewServer(st, sshx.Connector{})
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { s.Shutdown(); ln.Close() })
	return s
}

func renameActive(t *testing.T, app *App, sim tcell.SimulationScreen, name string) {
	t.Helper()
	app.app.QueueUpdate(func() { app.workspace.renameForm(app.workspace.activeNode()) })
	waitFor(t, app, sim, "Rename session")
	sim.InjectKey(tcell.KeyCtrlU, 0, tcell.ModCtrl)
	typeText(sim, name)
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitForGone(t, app, sim, "Rename session")
}

// Quit lazyssh with sessions open, start it again: the same sessions
// (same shells, output, names, layout and popups) are back.
func TestSessionsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	startService(t, dir)

	app, sim, done := runApp(t, dir, true)
	waitFor(t, app, sim, "$ ")
	time.Sleep(200 * time.Millisecond)
	typeText(sim, "MARK=persisted; echo before-restart-$((40+2))")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "before-restart-42")
	renameActive(t, app, sim, "web logs")

	clickText(t, app, sim, "⇆ Split right")
	waitFor(t, app, sim, "Split right — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	time.Sleep(300 * time.Millisecond)
	renameActive(t, app, sim, "db shell")

	clickText(t, app, sim, "❐ Popup")
	waitFor(t, app, sim, "Popup window — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "▁ Minimize")
	time.Sleep(300 * time.Millisecond)
	renameActive(t, app, sim, "build")
	typeText(sim, "echo in-the-popup")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "in-the-popup")
	var fx, fy int
	app.app.QueueUpdate(func() { f := app.workspace.floats[0]; fx, fy = f.x, f.y })

	// q: sessions keep running.
	app.app.QueueUpdate(func() { app.workspace.hide() })
	waitFor(t, app, sim, "Servers")
	typeText(sim, "q")
	waitFor(t, app, sim, "keep running in the background")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // "Quit — keep sessions"
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("lazyssh did not quit")
	}

	c, err := sessions.Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := c.List()
	c.Close()
	if len(list) != 3 {
		t.Fatalf("background sessions after quit = %d, want 3", len(list))
	}

	// Start again.
	app, sim, done = runApp(t, dir, true)
	waitFor(t, app, sim, "Welcome back")
	waitFor(t, app, sim, "before-restart-42") // output from before
	waitFor(t, app, sim, "in-the-popup")
	for _, name := range []string{"● 1 web logs", "● 2 db shell", "❐ build"} {
		waitFor(t, app, sim, name)
	}
	var tiles, floats, gx, gy int
	app.app.QueueUpdate(func() {
		tiles, floats = app.workspace.PaneCount(), len(app.workspace.floats)
		if floats > 0 {
			gx, gy = app.workspace.floats[0].x, app.workspace.floats[0].y
		}
	})
	if tiles != 2 || floats != 1 || gx != fx || gy != fy {
		t.Fatalf("restored %d tiles, %d popups at %d,%d (want 2, 1 at %d,%d)", tiles, floats, gx, gy, fx, fy)
	}
	// It is the same shell: the variable set before the restart is there.
	app.app.QueueUpdate(func() { app.workspace.focus(app.workspace.leaves()[0]) })
	time.Sleep(200 * time.Millisecond)
	typeText(sim, "echo still-$MARK")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "still-persisted")

	// q → End sessions and quit: nothing comes back next time.
	app.app.QueueUpdate(func() { app.workspace.hide() })
	waitFor(t, app, sim, "Servers")
	typeText(sim, "q")
	waitFor(t, app, sim, "keep running in the background")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // "End sessions and quit"
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("lazyssh did not quit")
	}
	c, _ = sessions.Dial(dir)
	list, _ = c.List()
	c.Close()
	if len(list) != 0 {
		t.Fatalf("sessions left after ending them: %d", len(list))
	}
	app, sim, done = runApp(t, dir, true)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ") // a fresh local terminal, no old layout
	if strings.Contains(readScreen(app, sim), "web logs") {
		t.Fatal("old layout came back after ending the sessions")
	}
}

// Without the service (or after a reboot) the layout and names come back
// with fresh sessions.
func TestLayoutRestoredWithoutService(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	app, sim, done := runApp(t, dir, false)
	waitFor(t, app, sim, "$ ")
	renameActive(t, app, sim, "left side")
	clickText(t, app, sim, "⇅ Split down")
	waitFor(t, app, sim, "Split down — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	time.Sleep(300 * time.Millisecond)
	renameActive(t, app, sim, "bottom")
	time.Sleep(500 * time.Millisecond) // layout is saved shortly after changes
	app.app.Stop()
	<-done

	app, sim, done = runApp(t, dir, false)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "● 1 left side")
	waitFor(t, app, sim, "● 2 bottom")
	var dir2 int
	app.app.QueueUpdate(func() { dir2 = app.workspace.root.dir })
	if dir2 != tview.FlexRow { // stacked
		t.Fatalf("split direction %d, want stacked", dir2)
	}
	waitFor(t, app, sim, "$ ")
	typeText(sim, "echo fresh-shell")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "fresh-shell")
}
