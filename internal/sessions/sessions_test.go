package sessions

import (
	"strings"
	"sync"
	"testing"
	"time"

	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

type sink struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *sink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out waiting for " + what)
}

func startServer(t *testing.T) (string, *Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, sshx.Connector{})
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { s.Shutdown(); ln.Close() })
	return dir, s
}

// A session keeps running after the window disconnects; a new window
// gets its output so far and can keep using it.
func TestSessionSurvivesDisconnect(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	dir, _ := startServer(t)

	c1, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := c1.Open(Spec{Label: "work", Cwd: "/tmp"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	out1 := &sink{}
	r1 := c1.Attach(id, out1, nil)
	_, _ = r1.Write([]byte("pwd; X=kept; echo first-$((20+1))\r"))
	eventually(t, "first output", func() bool { return strings.Contains(out1.String(), "first-21") })
	if !strings.Contains(out1.String(), "/tmp") {
		t.Fatalf("shell did not start in Cwd: %q", out1.String())
	}
	c1.Close() // lazyssh quits

	if _, err := Listen(dir); err == nil {
		t.Fatal("a second service could start while one is running")
	}

	c2, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	list, err := c2.List()
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].Spec.Label != "work" || list[0].Exited {
		t.Fatalf("list = %+v, %v", list, err)
	}
	ended := make(chan struct{})
	out2 := &sink{}
	r2 := c2.Attach(id, out2, func() { close(ended) })
	eventually(t, "replay", func() bool { return strings.Contains(out2.String(), "first-21") })
	// The same shell: its variable is still set.
	_, _ = r2.Write([]byte("echo still-$X\r"))
	eventually(t, "same shell", func() bool { return strings.Contains(out2.String(), "still-kept") })
	if err := r2.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	_, _ = r2.Write([]byte("stty size\r"))
	eventually(t, "resize", func() bool { return strings.Contains(out2.String(), "30 100") })

	// Closing it for good ends the program and removes it.
	if err := r2.Close(); err != nil {
		t.Fatal(err)
	}
	if list, _ := c2.List(); len(list) != 0 {
		t.Fatalf("still listed after close: %+v", list)
	}
}

func TestSessionExitIsReported(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir, _ := startServer(t)
	c, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id, _ := c.Open(Spec{}, 80, 24)
	ended := make(chan struct{})
	r := c.Attach(id, &sink{}, func() { close(ended) })
	_, _ = r.Write([]byte("exit\r"))
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("exit not reported")
	}
	// A window attaching later learns it ended too.
	ended2 := make(chan struct{})
	c.Attach(id, &sink{}, func() { close(ended2) })
	select {
	case <-ended2:
	case <-time.After(5 * time.Second):
		t.Fatal("exit not reported on reattach")
	}
}

func TestAltScreenTracking(t *testing.T) {
	alt := false
	trackAltScreen(&alt, []byte("hello\x1b[?1049h htop"))
	if !alt {
		t.Fatal("not on alt screen")
	}
	trackAltScreen(&alt, []byte("more output"))
	if !alt {
		t.Fatal("lost alt state")
	}
	trackAltScreen(&alt, []byte("\x1b[?1049h...\x1b[?1049l$ "))
	if alt {
		t.Fatal("still on alt screen")
	}
}
