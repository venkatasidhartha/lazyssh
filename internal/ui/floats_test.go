package ui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

func drag(s tcell.SimulationScreen, x0, y0, x1, y1 int) {
	s.InjectMouse(x0, y0, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	s.InjectMouse((x0+x1)/2, (y0+y1)/2, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	s.InjectMouse(x1, y1, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	s.InjectMouse(x1, y1, tcell.ButtonNone, tcell.ModNone)
	time.Sleep(150 * time.Millisecond)
}

// clickFloatButton clicks a title-bar button of the topmost popup.
func clickFloatButton(t *testing.T, a *App, s tcell.SimulationScreen, id string) {
	t.Helper()
	x, y := -1, -1
	a.app.QueueUpdate(func() {
		ws := a.workspace
		top := ws.floats[len(ws.floats)-1]
		for _, h := range ws.layer.hits {
			if h.id == id && h.node == top.node {
				x, y = h.x0+1, h.y
			}
		}
	})
	if x < 0 {
		t.Fatalf("no %s button on the popup:\n%s", id, readScreen(a, s))
	}
	s.InjectMouse(x, y, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	s.InjectMouse(x, y, tcell.ButtonNone, tcell.ModNone)
	time.Sleep(200 * time.Millisecond)
}

func TestWorkspacePopups(t *testing.T) {
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
	sim := tcell.NewSimulationScreen("UTF-8")
	app := New(st, &sshx.Connector{Key: key}, "test")
	app.app.SetScreen(sim)
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	sim.SetSize(140, 40)
	waitFor(t, app, sim, "$ ")

	type geo struct{ x, y, w, h, n, tiles int }
	state := func() (g geo) {
		app.app.QueueUpdate(func() {
			ws := app.workspace
			g.n, g.tiles = len(ws.floats), ws.PaneCount()
			if g.n > 0 {
				f := ws.floats[g.n-1]
				g.x, g.y, g.w, g.h = ws.layer.rect(f)
			}
		})
		return g
	}

	// ❐ Popup opens a session in a window above the tiles; typing goes there.
	clickText(t, app, sim, "❐ Popup")
	waitFor(t, app, sim, "Popup window — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "▁ Minimize")
	g := state()
	if g.n != 1 || g.tiles != 1 {
		t.Fatalf("popups=%d tiles=%d", g.n, g.tiles)
	}
	time.Sleep(300 * time.Millisecond)
	typeText(sim, "echo in-popup-$((1+1))")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "in-popup-2")
	x, y := findText(t, app, sim, "in-popup-2")
	if x < g.x || x >= g.x+g.w || y < g.y || y >= g.y+g.h {
		t.Fatalf("output at %d,%d is outside the popup %+v", x, y, g)
	}

	// Drag the title bar to move it, the ◢ corner to resize it.
	drag(sim, g.x+3, g.y, g.x-7, g.y-3)
	g2 := state()
	if g2.x != g.x-10 || g2.y != g.y-3 || g2.w != g.w {
		t.Fatalf("move: %+v -> %+v", g, g2)
	}
	drag(sim, g2.x+g2.w-1, g2.y+g2.h-1, g2.x+g2.w-11, g2.y+g2.h-3)
	g3 := state()
	if g3.w != g2.w-10 || g3.h != g2.h-2 || g3.x != g2.x {
		t.Fatalf("resize: %+v -> %+v", g2, g3)
	}

	// ▁ minimizes to the bottom bar; clicking it there brings it back.
	clickFloatButton(t, app, sim, "fmin")
	waitForGone(t, app, sim, "in-popup-2")
	waitFor(t, app, sim, "Minimized local")
	cx, cy := findText(t, app, sim, " ❐ local ")
	sim.InjectMouse(cx+2, cy, tcell.Button1, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
	sim.InjectMouse(cx+2, cy, tcell.ButtonNone, tcell.ModNone)
	waitFor(t, app, sim, "in-popup-2")

	// □ maximizes over the whole area, ❐ restores the old size.
	clickFloatButton(t, app, sim, "fmax")
	if g4 := state(); g4.w <= g3.w || g4.h <= g3.h {
		t.Fatalf("maximize: %+v -> %+v", g3, g4)
	}
	waitFor(t, app, sim, "❐ Restore")
	clickFloatButton(t, app, sim, "fmax")
	if g5 := state(); g5 != g3 {
		t.Fatalf("restore: %+v, want %+v", g5, g3)
	}

	// ⊞ docks it into the tiles; ❐ on a tile pops it back out.
	clickFloatButton(t, app, sim, "fdock")
	if g6 := state(); g6.n != 0 || g6.tiles != 2 {
		t.Fatalf("dock: %+v", g6)
	}
	clickPaneButton(t, app, sim, "float")
	if g7 := state(); g7.n != 1 || g7.tiles != 1 {
		t.Fatalf("pop out: %+v", g7)
	}
	waitFor(t, app, sim, "in-popup-2") // same session, output kept

	// A click on the tile behind types there again; ✕ closes the popup.
	clickFloatButton(t, app, sim, "fclose")
	if g8 := state(); g8.n != 0 || g8.tiles != 1 {
		t.Fatalf("close: %+v", g8)
	}
	var onTile bool
	app.app.QueueUpdate(func() { onTile = app.app.GetFocus() == app.workspace.focused.pane })
	if !onTile {
		t.Fatal("focus did not go back to the tile")
	}

	app.app.Stop()
	<-done
}

// A program that tracks every mouse move (htop, vim with mouse=a…) must
// not keep the mouse: the window's title buttons, move and resize still
// work after the mouse moved over it.
func TestPopupControlsWithMouseApp(t *testing.T) {
	app, sim, done := startApp(t)
	waitFor(t, app, sim, "$ ")
	grabMouse := func() {
		// What htop sends: any-event tracking with SGR coordinates.
		typeText(sim, `printf '\033[?1003h\033[?1006h'; echo grabbed`)
		sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	}
	wiggle := func(x0, y0, x1, y1 int) {
		for x := x0; x <= x1; x += max(1, (x1-x0)/15) {
			sim.InjectMouse(x, y0+(x-x0)%(y1-y0+1), tcell.ButtonNone, tcell.ModNone)
			time.Sleep(15 * time.Millisecond) // the simulated screen drops events past 10 queued
		}
		time.Sleep(150 * time.Millisecond)
	}

	// Tiled pane: after moving over it, its ⇆ button still works.
	grabMouse()
	waitFor(t, app, sim, "grabbed")
	var px, py, pw, ph int
	app.app.QueueUpdate(func() { px, py, pw, ph = app.workspace.focused.pane.GetRect() })
	wiggle(px+2, py+2, px+pw-3, py+ph-3)
	clickPaneButton(t, app, sim, "float") // pop it out
	var n int
	app.app.QueueUpdate(func() { n = len(app.workspace.floats) })
	if n != 1 {
		t.Fatal("pop out did not work while a program tracked the mouse")
	}

	// Popup: move it, resize it, minimize and close it.
	rect := func() (x, y, w, h int) {
		app.app.QueueUpdate(func() { x, y, w, h = app.workspace.layer.rect(app.workspace.floats[0]) })
		return
	}
	x, y, w, h := rect()
	wiggle(x+2, y+2, x+w-3, y+h-3)
	drag(sim, x+3, y, x-5, y-2)
	if x2, y2, _, _ := rect(); x2 != x-8 || y2 != y-2 {
		t.Fatalf("move: %d,%d -> %d,%d", x, y, x2, y2)
	}
	x, y, w, h = rect()
	wiggle(x+2, y+2, x+w-3, y+h-3)
	drag(sim, x+w-1, y+h-1, x+w-6, y+h-2)
	if _, _, w2, h2 := rect(); w2 != w-5 || h2 != h-1 {
		t.Fatalf("resize: %dx%d -> %dx%d", w, h, w2, h2)
	}
	wiggle(x+2, y+2, x+w-8, y+h-3)
	clickFloatButton(t, app, sim, "fclose")
	app.app.QueueUpdate(func() { n = len(app.workspace.floats) })
	if n != 0 {
		t.Fatal("✕ did not close the popup while a program tracked the mouse")
	}
	app.app.Stop()
	<-done
}
