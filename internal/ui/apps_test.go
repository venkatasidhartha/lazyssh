package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// TestFullScreenApps runs real full-screen programs inside a popup and
// checks that they draw, take keys, survive a terminal resize, leave the
// window controls usable and give the shell back cleanly. It needs the
// programs installed, so it only runs with LAZYSSH_APP_TESTS=1.
func TestFullScreenApps(t *testing.T) {
	if os.Getenv("LAZYSSH_APP_TESTS") == "" {
		t.Skip("set LAZYSSH_APP_TESTS=1 to run real full-screen programs")
	}
	type key struct {
		k tcell.Key
		r rune
	}
	esc, enter, ctrlC, ctrlX := key{tcell.KeyEsc, 0}, key{tcell.KeyEnter, 0}, key{tcell.KeyCtrlC, 0}, key{tcell.KeyCtrlX, 0}
	text := func(s string) []key {
		var ks []key
		for _, r := range s {
			ks = append(ks, key{tcell.KeyRune, r})
		}
		return ks
	}
	cat := func(parts ...[]key) []key {
		var ks []key
		for _, p := range parts {
			ks = append(ks, p...)
		}
		return ks
	}
	tmp := t.TempDir()
	apps := []struct {
		name, cmd string
		ready     string // shown once it is up
		input     []key  // typed while it runs
		after     string // shown after the input
		quit      []key
	}{
		{"htop", "htop", "F1Help", text("/"), "Search:", cat([]key{esc}, text("q"))},
		{"top", "top", "load average", text("h"), "Toggle:", text("qq")},
		{"less", `printf 'LESS-%s\n' $(seq 300) | less`, "LESS-1 ", text("G"), "LESS-300", text("q")},
		{"more", `printf 'MORE-%s\n' $(seq 300) | more`, "--More--", text(" "), "MORE-25", text("q")},
		{"man", "man 1 sh", "NAME", text("/DESCRIPTION\n"), "Manual page sh(1)", text("q")},
		{"vi", "vi -u NONE -n " + tmp + "/vi.txt", "~", cat(text("ihello from vi"), []key{esc}), "hello from vi", text(":wq\n")},
		{"nvim", "nvim --clean -n " + tmp + "/nvim.txt", "~", cat(text("ihello from nvim"), []key{esc}), "hello from nvim", text(":wq\n")},
		{"nano", "nano " + tmp + "/nano.txt", "^X Exit", text("hello from nano"), "hello from nano", cat([]key{ctrlX}, text("y"), []key{enter})},
		{"watch", "watch -n1 echo WATCHED", "Every 1.0s", nil, "WATCHED", []key{ctrlC}},
		{"screen", "screen -q", "", text("echo INSIDE-$((6*7))\n"), "INSIDE-42", text("exit\n")},
		{"python3", "python3 -q", ">>> ", text("print(6*7)\n"), "42", text("exit()\n")},
		{"nmtui", "nmtui", "NetworkManager", nil, "Quit", []key{esc}},
		{"alsamixer", "alsamixer", "AlsaMixer", nil, "Esc: Exit", []key{esc}},
	}

	app, sim, done := startApp(t)
	defer func() { app.app.Stop(); <-done }()
	waitFor(t, app, sim, "$ ")
	press := func(ks []key) {
		for _, k := range ks {
			if k.k == tcell.KeyRune && k.r == '\n' {
				sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			} else {
				sim.InjectKey(k.k, k.r, tcell.ModNone)
			}
			time.Sleep(15 * time.Millisecond)
		}
	}
	shows := func(want string, d time.Duration) bool {
		for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if strings.Contains(readScreen(app, sim), want) {
				return true
			}
		}
		return false
	}
	rect := func() (x, y, w, h int) {
		app.app.QueueUpdate(func() { x, y, w, h = app.workspace.layer.rect(app.workspace.floats[0]) })
		return
	}
	resize := func(w, h int) {
		sim.SetSize(w, h)
		_ = sim.PostEvent(tcell.NewEventResize(w, h))
		time.Sleep(300 * time.Millisecond)
	}

	var report []string
	for _, a := range apps {
		ok := t.Run(a.name, func(t *testing.T) {
			first := strings.Fields(a.cmd)[0]
			if _, err := exec.LookPath(first); err != nil {
				t.Skip(first + " not installed")
			}
			// A fresh popup for every program.
			clickText(t, app, sim, "❐ Popup")
			waitFor(t, app, sim, "Popup window — pick a server")
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			waitFor(t, app, sim, "▁ Minimize")
			time.Sleep(300 * time.Millisecond)
			defer func() {
				app.app.QueueUpdate(func() {
					for len(app.workspace.floats) > 0 {
						app.workspace.closeFloat(app.workspace.floats[0])
					}
				})
				resize(140, 40)
			}()

			press(text(a.cmd + "\n"))
			if a.ready != "" && !shows(a.ready, 5*time.Second) {
				t.Fatalf("never drew %q:\n%s", a.ready, readScreen(app, sim))
			}
			time.Sleep(300 * time.Millisecond)
			press(a.input)
			if !shows(a.after, 5*time.Second) {
				t.Fatalf("keys did not work, %q not shown:\n%s", a.after, readScreen(app, sim))
			}

			// Mouse over the program, then the window controls must work.
			x, y, w, h := rect()
			for i := 0; i < 12; i++ {
				sim.InjectMouse(x+3+i*4, y+3+i%4, tcell.ButtonNone, tcell.ModNone)
				time.Sleep(15 * time.Millisecond)
			}
			sim.InjectMouse(x+w/2, y+h/2, tcell.WheelDown, tcell.ModNone)
			time.Sleep(100 * time.Millisecond)
			drag(sim, x+3, y, x-4, y-1)
			if x2, y2, _, _ := rect(); x2 != x-7 || y2 != y-1 {
				t.Errorf("could not move the window: %d,%d -> %d,%d\n%s", x, y, x2, y2, readScreen(app, sim))
			}

			// Full screen and back: the program redraws at each size.
			clickFloatButton(t, app, sim, "fzoom")
			if !shows("⤡ Exit full screen", 3*time.Second) {
				t.Fatalf("full screen did not open:\n%s", readScreen(app, sim))
			}
			if a.after != "" && !shows(a.after, 3*time.Second) && !shows(a.ready, time.Second) {
				t.Errorf("lost its screen in full screen:\n%s", readScreen(app, sim))
			}
			clickText(t, app, sim, "⤡ Exit full screen")
			if !shows("▁ Minimize", 3*time.Second) {
				t.Errorf("did not come back from full screen:\n%s", readScreen(app, sim))
			}

			// The terminal gets smaller and bigger again (font zoom does
			// this): the program must redraw at the new size.
			resize(110, 32)
			resize(140, 40)
			time.Sleep(500 * time.Millisecond)
			if a.after != "" && !shows(a.after, 3*time.Second) && !shows(a.ready, time.Second) {
				t.Errorf("lost its screen after a resize:\n%s", readScreen(app, sim))
			}

			press(a.quit)
			if !shows("$ ", 5*time.Second) {
				t.Fatalf("did not give the shell back:\n%s", readScreen(app, sim))
			}
			time.Sleep(300 * time.Millisecond)
			// The PTY size matches the window after all the resizing.
			press(text("clear; stty size\n"))
			var iw, ih int
			app.app.QueueUpdate(func() { _, _, iw, ih = app.workspace.floats[0].node.pane.GetInnerRect() })
			if want := fmt.Sprintf("%d %d", ih, iw); !shows(want, 3*time.Second) {
				t.Errorf("stty size is not %q:\n%s", want, readScreen(app, sim))
			}
		})
		status := "ok"
		if !ok {
			status = "FAILED"
		}
		report = append(report, fmt.Sprintf("%-10s %s", a.name, status))
	}
	t.Log("\n" + strings.Join(report, "\n"))
}
