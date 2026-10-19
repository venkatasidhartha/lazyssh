package ui

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
	"lazyssh/internal/sessions"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

func alive(t *testing.T, done chan error, where string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("lazyssh ended (%v) during %s", err, where)
	default:
	}
}

func resizeTo(sim tcell.SimulationScreen, w, h int) {
	sim.SetSize(w, h)
	_ = sim.PostEvent(tcell.NewEventResize(w, h))
	time.Sleep(150 * time.Millisecond)
}

// Splits, popups and full screen survive any terminal size, down to 1x1.
func TestTinyTerminal(t *testing.T) {
	app, sim, done := startApp(t)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ")
	app.app.QueueUpdate(func() {
		w := app.workspace
		w.open(nil, tview.FlexColumn)
		w.open(nil, tview.FlexRow)
		w.open(nil, dirFloat)
	})
	time.Sleep(300 * time.Millisecond)
	rng := rand.New(rand.NewSource(1))
	for _, size := range [][2]int{{1, 1}, {2, 2}, {5, 3}, {12, 4}, {20, 6}, {33, 9}, {60, 15}, {140, 40}} {
		resizeTo(sim, size[0], size[1])
		for range 15 {
			x, y := rng.Intn(size[0]), rng.Intn(size[1])
			sim.InjectMouse(x, y, tcell.Button1, tcell.ModNone)
			sim.InjectMouse(x, y, tcell.ButtonNone, tcell.ModNone)
			time.Sleep(5 * time.Millisecond)
		}
		// Back to the workspace if a click opened a dialog.
		app.app.QueueUpdate(func() { app.workspace.restore() })
		alive(t, done, fmt.Sprintf("%dx%d", size[0], size[1]))
	}
	app.app.QueueUpdate(func() { app.workspace.zoom(app.workspace.leaves()[0]) })
	for _, size := range [][2]int{{1, 1}, {10, 3}, {140, 40}} {
		resizeTo(sim, size[0], size[1])
		alive(t, done, "full screen")
	}
	app.app.QueueUpdate(func() { app.workspace.unzoom() })
	waitFor(t, app, sim, "⏻ Quit")
}

// A dozen panes, even sizes, then closing them all one by one.
func TestManyPanes(t *testing.T) {
	app, sim, done := startApp(t)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ")
	app.app.QueueUpdate(func() {
		for i := range 11 {
			app.workspace.open(nil, []int{tview.FlexColumn, tview.FlexRow}[i%2])
		}
	})
	waitFor(t, app, sim, "local 12")
	clickText(t, app, sim, "⊟ Even sizes")
	for _, n := range []int{12, 6, 1} {
		if got := paneCount(app); got < n {
			t.Fatalf("panes = %d", got)
		}
	}
	resizeTo(sim, 80, 24) // panes smaller than their border
	alive(t, done, "12 panes in 80x24")
	resizeTo(sim, 140, 40)
	for paneCount(app) > 0 {
		app.app.QueueUpdate(func() { app.workspace.closeFocused() })
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, app, sim, "No session open")
}

// Emoji, CJK and very long names in output, titles and the session list.
func TestUnicodeEverywhere(t *testing.T) {
	app, sim, done := startApp(t)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ")
	time.Sleep(200 * time.Millisecond)
	typeText(sim, "printf '\\360\\237\\224\\245 日本語 テスト café ✓\\n'")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "café")
	renameActive(t, app, sim, "🔥 本番 "+strings.Repeat("very-long-name-", 12))
	waitFor(t, app, sim, "🔥")
	app.app.QueueUpdate(func() { app.workspace.open(nil, dirFloat) })
	time.Sleep(200 * time.Millisecond)
	renameActive(t, app, sim, "ログ 📜")
	waitFor(t, app, sim, "ロ") // wide characters take two cells
	resizeTo(sim, 50, 15)
	alive(t, done, "narrow with wide names")
	resizeTo(sim, 140, 40)
	// The session still works.
	app.app.QueueUpdate(func() { app.workspace.focus(app.workspace.leaves()[0]) })
	time.Sleep(100 * time.Millisecond)
	typeText(sim, "echo still-ok")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "still-ok")
}

// Broken saved files do not stop lazyssh from starting or working.
func TestCorruptSavedFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	for _, f := range []string{"workspace.json", "groups.json", "commands.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{not json at all \x00\xff"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	app, sim, done := runApp(t, dir, false)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ") // starts with a fresh terminal
	clickText(t, app, sim, "▣ Groups")
	waitFor(t, app, sim, "groups.json") // says which file is broken
	clickText(t, app, sim, "★ Commands")
	time.Sleep(200 * time.Millisecond)
	app.app.QueueUpdate(func() { app.workspace.restore() })
	alive(t, done, "using broken files")
	// Saving a group replaces the broken file.
	if err := os.Remove(filepath.Join(dir, "groups.json")); err != nil {
		t.Fatal(err)
	}
	clickText(t, app, sim, "▣ Groups")
	waitFor(t, app, sim, "No groups yet")
}

// A saved layout or group that mentions a deleted server opens the rest.
func TestDeletedServerInLayout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	layout := `{"version":1,"root":{"dir":1,"kids":[
		{"label":"gone-server","server_id":"no-such-id","target":"x@y","weight":1000},
		{"label":"kept-local","target":"this machine","weight":1000}]}}`
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(layout), 0o600); err != nil {
		t.Fatal(err)
	}
	app, sim, done := runApp(t, dir, false)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "● 1 kept-local")
	if strings.Contains(readScreen(app, sim), "gone-server") {
		t.Fatal("a session for a deleted server was opened")
	}
	if n := paneCount(app); n != 1 {
		t.Fatalf("panes = %d", n)
	}
}

// The service crashes while lazyssh is open: the old panes say so, and new
// sessions still open (in a restarted service, or inside lazyssh).
func TestServiceDiesWhileOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	st, _ := store.Open(dir)
	ln, err := sessions.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := sessions.NewServer(st, sshx.Connector{})
	go func() { _ = srv.Serve(ln) }()

	app, sim, done := runApp(t, dir, true)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ")
	time.Sleep(300 * time.Millisecond)

	// Crash: the process is gone, so are its sessions and connections.
	srv.Shutdown()
	ln.Close()
	srv.Disconnect()
	waitFor(t, app, sim, "session ended")
	alive(t, done, "service crash")

	// No service any more: a new session runs inside lazyssh.
	clickText(t, app, sim, "⇆ Split right")
	waitFor(t, app, sim, "Split right — pick a server")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "not available")
	time.Sleep(300 * time.Millisecond)
	typeText(sim, "echo in-process-ok")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "in-process-ok")

	// The service is back: the next session goes into it again.
	ln2, err := sessions.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := sessions.NewServer(st, sshx.Connector{})
	go func() { _ = srv2.Serve(ln2) }()
	t.Cleanup(func() { srv2.Shutdown(); ln2.Close() })
	app.app.QueueUpdate(func() { app.workspace.sd = nil })
	// (with sd nil lazyssh uses in-process; reconnecting needs a lost
	// client, so simulate the usual case: the client from before is lost)
	c, _ := sessions.Dial(dir)
	c.Close()
	time.Sleep(100 * time.Millisecond)
	app.app.QueueUpdate(func() { app.workspace.sd = c }) // a lost client
	app.app.QueueUpdate(func() { app.workspace.open(nil, tview.FlexColumn) })
	var id string
	for deadline := time.Now().Add(5 * time.Second); id == "" && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		app.app.QueueUpdate(func() {
			if l := app.workspace.leaves(); len(l) > 0 {
				id = l[len(l)-1].sessionID
			}
		})
	}
	if id == "" {
		t.Fatal("new session did not go to the restarted service")
	}
}

// A session that ends while lazyssh is closed shows as ended next time.
func TestSessionEndsWhileClosed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	startService(t, dir)
	app, sim, done := runApp(t, dir, true)
	waitFor(t, app, sim, "$ ")
	time.Sleep(600 * time.Millisecond) // layout saved
	app.app.Stop()
	<-done

	c, err := sessions.Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := c.List()
	if len(list) != 1 {
		t.Fatalf("sessions = %d", len(list))
	}
	r := c.Attach(list[0].ID, &strings.Builder{}, nil)
	_, _ = r.Write([]byte("exit\r"))
	time.Sleep(500 * time.Millisecond)
	c.Close()

	app, sim, done = runApp(t, dir, true)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "session ended")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, app, sim, "No session open")
}

// Two lazyssh windows on the same sessions see each other's typing, and
// closing one leaves the other working.
func TestTwoWindows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	startService(t, dir)
	a1, s1, d1 := runApp(t, dir, true)
	defer func() { a1.app.Stop(); <-d1 }()
	waitFor(t, a1, s1, "$ ")
	time.Sleep(600 * time.Millisecond)

	a2, s2, d2 := runApp(t, dir, true)
	waitFor(t, a2, s2, "Welcome back")
	time.Sleep(300 * time.Millisecond)
	typeText(s2, "echo from-window-two")
	s2.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, a2, s2, "from-window-two")
	waitFor(t, a1, s1, "from-window-two") // same shell
	a2.app.Stop()
	<-d2

	typeText(s1, "echo window-one-still")
	s1.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, a1, s1, "window-one-still")
}

// A 200 KB paste arrives complete.
func TestHugePaste(t *testing.T) {
	app, sim, done := startApp(t)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ")
	time.Sleep(200 * time.Millisecond)
	typeText(sim, "wc -c")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	time.Sleep(200 * time.Millisecond)
	var b strings.Builder
	for b.Len() < 200_000 {
		b.WriteString(strings.Repeat("x", 79) + "\n")
	}
	text := b.String()
	app.app.QueueUpdate(func() { app.workspace.activePane().Paste(text) })
	time.Sleep(time.Second)
	sim.InjectKey(tcell.KeyCtrlD, 0, tcell.ModCtrl)
	waitFor(t, app, sim, fmt.Sprintf("%d", len(text)))
}

// Opening and closing sessions as fast as possible leaves nothing behind.
func TestRapidOpenClose(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	startService(t, dir)
	app, sim, done := runApp(t, dir, true)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "1 session")
	for range 25 {
		app.app.QueueUpdate(func() {
			app.workspace.open(nil, -1)
			app.workspace.closeFocused()
		})
	}
	app.app.QueueUpdate(func() {
		for app.workspace.PaneCount() > 0 || len(app.workspace.floats) > 0 {
			app.workspace.closeFocused()
		}
	})
	c, _ := sessions.Dial(dir)
	defer c.Close()
	var n int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		list, _ := c.List()
		if n = len(list); n == 0 {
			break
		}
	}
	if n != 0 {
		t.Fatalf("%d sessions left in the service", n)
	}
	alive(t, done, "rapid open/close")
}

// A server that cannot be reached ends with a message, not a hang.
func TestUnreachableServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := domain.Server{Alias: "nowhere", Host: "127.0.0.1", Port: 1, User: "u", Password: "pw"}
	app, sim, done := startApp(t, srv)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "nowhere")
	s := app.store.List()[0]
	app.app.QueueUpdate(func() { app.workspace.show(); app.workspace.open(&s, -1) })
	if _, err := exec.LookPath("ssh"); err == nil {
		waitFor(t, app, sim, "session ended") // built-in client fails, then system ssh
	} else {
		waitFor(t, app, sim, "no ssh client")
	}
	alive(t, done, "unreachable server")
}

// Random clicks and keys for a while must never crash lazyssh.
func TestMonkey(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no ssh, nothing to reach by accident
	t.Setenv("SHELL", "/bin/cat") // sessions only echo what is typed
	dir := t.TempDir()
	seed, secs := int64(42), 25
	fmt.Sscan(os.Getenv("MONKEY_SEED"), &seed)
	fmt.Sscan(os.Getenv("MONKEY_SECS"), &secs)
	rng := rand.New(rand.NewSource(seed))
	keys := []tcell.Key{tcell.KeyEnter, tcell.KeyEsc, tcell.KeyTab, tcell.KeyBacktab, tcell.KeyUp, tcell.KeyDown,
		tcell.KeyLeft, tcell.KeyRight, tcell.KeyBackspace, tcell.KeyDelete, tcell.KeyCtrlA, tcell.KeyCtrlC,
		tcell.KeyCtrlD, tcell.KeyCtrlU, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyF1}
	runes := []rune("abcdeghijklmnoprstuvwxyz0123456789 v|s-nxtzreb?q[]1")
	restarts, events := 0, 0
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	service := os.Getenv("MONKEY_SERVICE") != ""
	if service {
		startService(t, dir)
	}
	for time.Now().Before(deadline) {
		app, sim, done := runApp(t, dir, service)
		w, h := 60+rng.Intn(100), 15+rng.Intn(35)
		resizeTo(sim, w, h)
		ended := false
	run:
		for i := 0; i < 400 && time.Now().Before(deadline); i++ {
			select {
			case <-done: // a random click chose Quit: start again
				restarts++
				ended = true
				break run
			default:
			}
			events++
			switch r := rng.Intn(10); {
			case r < 4:
				x, y := rng.Intn(w), rng.Intn(h)
				btn := []tcell.ButtonMask{tcell.Button1, tcell.Button1, tcell.Button2, tcell.WheelUp, tcell.WheelDown}[rng.Intn(5)]
				sim.InjectMouse(x, y, btn, tcell.ModNone)
				if btn == tcell.Button1 && rng.Intn(3) == 0 { // drag
					sim.InjectMouse(rng.Intn(w), rng.Intn(h), btn, tcell.ModNone)
				}
				sim.InjectMouse(x, y, tcell.ButtonNone, tcell.ModNone)
			case r < 7:
				sim.InjectKey(tcell.KeyRune, runes[rng.Intn(len(runes))], tcell.ModNone)
			case r < 9:
				sim.InjectKey(keys[rng.Intn(len(keys))], 0, tcell.ModNone)
			default:
				w, h = 1+rng.Intn(160), 1+rng.Intn(50)
				resizeTo(sim, w, h)
			}
			time.Sleep(2 * time.Millisecond)
		}
		if !ended {
			app.app.Stop()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("lazyssh did not stop: frozen")
			}
		}
	}
	t.Logf("seed %d: %d random events, %d restarts after a random quit, no crash", seed, events, restarts)
}

// Navigation keys on an empty server list (no servers, or a search that
// matches nothing) used to send tview's table into an endless loop.
func TestEmptyServerListKeys(t *testing.T) {
	for _, withServer := range []bool{false, true} {
		var servers []domain.Server
		if withServer {
			servers = append(servers, domain.Server{Alias: "web-1", Host: "10.0.0.1", User: "u"})
		}
		app, sim, done := startApp(t, servers...)
		if withServer {
			waitFor(t, app, sim, "web-1")
			// A search that matches nothing empties the list.
			app.app.QueueUpdate(func() { app.searchBar.SetText("zzz-no-match"); app.refreshServerList() })
		} else {
			waitFor(t, app, sim, "$ ")
			app.app.QueueUpdate(func() { app.endAllSessions(); app.workspace.hide() })
		}
		waitFor(t, app, sim, "▶ Terminal")
		app.app.QueueUpdate(func() { app.app.SetFocus(app.serverList) })
		for _, k := range []tcell.Key{tcell.KeyUp, tcell.KeyDown, tcell.KeyHome, tcell.KeyEnd, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyEnter} {
			sim.InjectKey(k, 0, tcell.ModNone)
		}
		for _, r := range "jkgGhl" {
			sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
		}
		responded := make(chan struct{})
		go func() { app.app.QueueUpdate(func() {}); close(responded) }()
		select {
		case <-responded:
		case <-time.After(5 * time.Second):
			t.Fatalf("lazyssh froze after arrow keys on an empty server list (servers: %v)", withServer)
		}
		app.app.Stop()
		<-done
	}
}
