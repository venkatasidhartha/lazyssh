package vterm

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"golang.org/x/crypto/ssh"
)

// fakeServer is an SSH server whose shell prints a banner and echoes input.
type fakeServer struct {
	addr string
	mu   sync.Mutex
	size [2]uint32 // last window-change cols, rows
}

func startFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	fs := &fakeServer{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fs.serve(c, cfg)
		}
	}()
	return fs
}

func (fs *fakeServer) serve(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, _ := nc.Accept()
		go func() {
			for r := range creqs {
				switch r.Type {
				case "pty-req", "shell":
					_ = r.Reply(true, nil)
					if r.Type == "shell" {
						_, _ = ch.Write([]byte("\x1b[1mwelcome\x1b[0m\r\n"))
						go func() {
							buf := make([]byte, 256)
							for {
								n, err := ch.Read(buf)
								if err != nil {
									return
								}
								_, _ = ch.Write(buf[:n])
								if strings.Contains(string(buf[:n]), "bye") {
									_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
									ch.Close()
									return
								}
							}
						}()
					}
				case "window-change":
					fs.mu.Lock()
					fs.size = [2]uint32{binary.BigEndian.Uint32(r.Payload), binary.BigEndian.Uint32(r.Payload[4:])}
					fs.mu.Unlock()
				default:
					_ = r.Reply(false, nil)
				}
			}
		}()
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out waiting for " + what)
}

func TestStartSSH(t *testing.T) {
	fs := startFakeServer(t)
	client, err := ssh.Dial("tcp", fs.addr, &ssh.ClientConfig{User: "u", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	p := NewPane("fake", nil)
	if err := StartSSH(p, client); err != nil {
		t.Fatal(err)
	}
	eventually(t, "banner", func() bool { return strings.Contains(p.ScreenText(), "welcome") })

	p.Send([]byte("hi there"))
	eventually(t, "echo", func() bool { return strings.Contains(p.CursorLine(), "hi there") })

	// Resizing the pane resizes the remote PTY.
	p.mu.Lock()
	be := p.be
	p.mu.Unlock()
	_ = be.Resize(100, 30)
	eventually(t, "window-change", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.size == [2]uint32{100, 30}
	})

	p.Send([]byte(" bye"))
	eventually(t, "exit", p.IsExited)
}

func TestKeyBytes(t *testing.T) {
	cases := []struct {
		ev        *tcell.EventKey
		appCursor bool
		want      string
	}{
		{tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone), false, "a"},
		{tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModAlt), false, "\x1bx"},
		{tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModCtrl), false, "\x03"},
		{tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), false, "\r"},
		{tcell.NewEventKey(tcell.KeyBackspace2, 0, tcell.ModNone), false, "\x7f"},
		{tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone), false, "\x1b[A"},
		{tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone), true, "\x1bOA"},
		{tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModCtrl), false, "\x1b[1;5D"},
		{tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone), false, "\x1b[6~"},
		{tcell.NewEventKey(tcell.KeyF5, 0, tcell.ModNone), false, "\x1b[15~"},
		{tcell.NewEventKey(tcell.KeyCtrlB, 0, tcell.ModCtrl), false, "\x02"},
		{tcell.NewEventKey(tcell.KeyRune, 0x04, tcell.ModNone), false, "\x04"}, // raw ^D from the terminal
		{tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), false, "\t"},
		{tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone), false, "\x1b"},
	}
	for _, c := range cases {
		if got := string(keyBytes(c.ev, c.appCursor)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.ev.Name(), got, c.want)
		}
	}
}

type recordBackend struct {
	mu  sync.Mutex
	got strings.Builder
}

func (r *recordBackend) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got.Write(p)
}
func (r *recordBackend) Resize(int, int) error { return nil }
func (r *recordBackend) Close() error          { return nil }
func (r *recordBackend) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got.String()
}

// A program that enables mouse tracking gets clicks and wheel events;
// otherwise the wheel scrolls the pane's history.
func TestMouseForwarding(t *testing.T) {
	p := NewPane("m", nil)
	p.SetRect(0, 0, 42, 12) // inner 40x10
	rec := &recordBackend{}
	p.Attach(rec)
	sim := tcell.NewSimulationScreen("UTF-8")
	_ = sim.Init()
	sim.SetSize(42, 12)
	p.Draw(sim)
	h := p.MouseHandler()
	noFocus := func(tview.Primitive) {}

	// No mouse mode: the wheel scrolls history instead of sending input.
	for i := range 50 {
		_, _ = p.Output().Write([]byte(itoa(i) + "\r\n"))
	}
	p.Draw(sim)
	h(tview.MouseScrollUp, tcell.NewEventMouse(5, 5, tcell.WheelUp, 0), noFocus)
	if p.scroll != wheelLines || rec.String() != "" {
		t.Fatalf("scroll=%d sent=%q", p.scroll, rec.String())
	}
	p.scrollToBottom()

	// SGR mouse mode (what htop/vim use).
	_, _ = p.Output().Write([]byte("\x1b[?1000h\x1b[?1006h"))
	h(tview.MouseLeftDown, tcell.NewEventMouse(4, 3, tcell.Button1, 0), noFocus)
	h(tview.MouseLeftUp, tcell.NewEventMouse(4, 3, tcell.ButtonNone, 0), noFocus)
	h(tview.MouseScrollDown, tcell.NewEventMouse(4, 3, tcell.WheelDown, 0), noFocus)
	want := "\x1b[<0;4;3M\x1b[<0;4;3m\x1b[<65;4;3M"
	eventually(t, "mouse bytes", func() bool { return rec.String() == want })
	if p.scroll != 0 {
		t.Fatal("wheel scrolled history while the program wanted the mouse")
	}
}
