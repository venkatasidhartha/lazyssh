package ui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// ⏻ Quit leaves lazyssh from the workspace and from the server list.
func TestQuitButton(t *testing.T) {
	app, sim, done := startApp(t)
	waitFor(t, app, sim, "$ ")
	clickText(t, app, sim, "⏻ Quit")
	waitFor(t, app, sim, "still running")
	clickLastText(t, app, sim, "Quit")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("⏻ Quit did not leave lazyssh")
	}

	// From the server list, with nothing open, it quits right away.
	app, sim, done = startApp(t)
	waitFor(t, app, sim, "$ ")
	app.app.QueueUpdate(func() { app.endAllSessions(); app.workspace.hide() })
	waitFor(t, app, sim, "▶ Terminal")
	clickText(t, app, sim, "⏻ Quit")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("⏻ Quit on the server list did not leave lazyssh")
	}
}

// Save the open sessions as a group, then open them all with one click.
func TestSessionGroups(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	app, sim, done := runApp(t, dir, false)
	waitFor(t, app, sim, "$ ")
	renameActive(t, app, sim, "web logs")
	clickText(t, app, sim, "⇆ Split right")
	waitFor(t, app, sim, "Split right — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	time.Sleep(300 * time.Millisecond)
	renameActive(t, app, sim, "db")
	clickText(t, app, sim, "❐ Popup")
	waitFor(t, app, sim, "Popup window — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "▁ Minimize")
	time.Sleep(200 * time.Millisecond)
	renameActive(t, app, sim, "build")

	clickText(t, app, sim, "▣ Groups")
	waitFor(t, app, sim, "No groups yet")
	clickLastText(t, app, sim, "+ Save open sessions")
	waitFor(t, app, sim, "Save open sessions as a group")
	typeText(sim, "prod")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "3 sessions: web logs, db, build")
	sim.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)
	waitFor(t, app, sim, "▣ prod") // in the session list

	// End everything, then open the group from the session list.
	app.app.QueueUpdate(func() { app.endAllSessions(); app.workspace.refresh() })
	waitFor(t, app, sim, "No session open")
	clickText(t, app, sim, "▣ prod")
	waitFor(t, app, sim, "Opened group prod")
	for _, name := range []string{"● 1 web logs", "● 2 db", "❐ build"} {
		waitFor(t, app, sim, name)
	}
	var tiles, floats int
	app.app.QueueUpdate(func() { tiles, floats = app.workspace.PaneCount(), len(app.workspace.floats) })
	if tiles != 2 || floats != 1 {
		t.Fatalf("group opened %d tiles, %d popups", tiles, floats)
	}
	time.Sleep(300 * time.Millisecond)
	typeText(sim, "echo group-session-ok")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "group-session-ok")

	// Opening it again adds its sessions next to the open ones.
	clickText(t, app, sim, "▣ prod")
	waitFor(t, app, sim, "web logs 2")
	app.app.QueueUpdate(func() { tiles, floats = app.workspace.PaneCount(), len(app.workspace.floats) })
	if tiles != 4 || floats != 2 {
		t.Fatalf("second open: %d tiles, %d popups", tiles, floats)
	}

	// The group is still there after a restart, and can be removed.
	app.app.Stop()
	<-done
	app, sim, done = runApp(t, dir, false)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "▣ prod")
	app.app.QueueUpdate(func() { app.workspace.groupManager() })
	waitFor(t, app, sim, "Session groups")
	clickLastText(t, app, sim, "✕ Remove")
	clickLastText(t, app, sim, "✕ Yes, remove")
	waitFor(t, app, sim, "No groups yet")
}
