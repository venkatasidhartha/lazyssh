package sessions

import (
	"encoding/gob"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"lazyssh/internal/domain"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

// A socket left behind by a crashed service does not stop a new one.
func TestStaleSocketIsReplaced(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // the file stays, nobody listens: like after kill -9
	if _, err := os.Stat(SocketPath(dir)); err != nil {
		t.Fatal("stale socket file missing:", err)
	}
	ln2, err := Listen(dir)
	if err != nil {
		t.Fatal("could not replace a stale socket:", err)
	}
	ln2.Close()
}

// Attaching to a session that is gone (the service restarted, or it was
// closed in another window) reports it as ended instead of hanging.
func TestAttachUnknownSession(t *testing.T) {
	dir, _ := startServer(t)
	c, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out := &sink{}
	ended := make(chan struct{})
	c.Attach("does-not-exist", out, func() { close(ended) })
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("attach to a missing session never ended")
	}
	if !strings.Contains(out.String(), "no longer") {
		t.Fatalf("no explanation shown: %q", out.String())
	}
	// Input, resize and close for unknown ids are harmless.
	r := c.Attach("also-missing", &sink{}, nil)
	_, _ = r.Write([]byte("x"))
	_ = r.Resize(10, 10)
	_ = r.Close()
	if _, err := c.List(); err != nil {
		t.Fatal("service broken after unknown ids:", err)
	}
}

// Rubbish on the socket only drops that connection.
func TestGarbageOnSocket(t *testing.T) {
	dir, _ := startServer(t)
	raw, err := net.Dial("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = raw.Write([]byte("GET / HTTP/1.1\r\n\r\n\x00\xff\xfe garbage"))
	raw.Close()
	c, err := Dial(dir)
	if err != nil {
		t.Fatal("service died after garbage:", err)
	}
	defer c.Close()
	if _, err := c.List(); err != nil {
		t.Fatal(err)
	}
}

// Huge output keeps memory bounded and a window that stops reading does
// not hold up the session or other windows.
func TestFloodAndStuckWindow(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir, srv := startServer(t)

	// A window that attaches and then never reads.
	stuck, err := net.Dial("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close()
	enc := gob.NewEncoder(stuck)

	c, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id, _ := c.Open(Spec{}, 80, 24)
	_ = enc.Encode(Msg{Kind: "attach", ID: id})
	out := &sink{}
	r := c.Attach(id, out, nil)
	// About 30 MB of output.
	_, _ = r.Write([]byte("head -c 30000000 /dev/zero | tr '\\0' x; echo; echo FLOOD-DONE\r"))
	deadline := time.Now().Add(60 * time.Second)
	for !strings.Contains(out.String(), "FLOOD-DONE") {
		if time.Now().After(deadline) {
			t.Fatal("flood never finished (session blocked by the stuck window?)")
		}
		time.Sleep(50 * time.Millisecond)
	}
	se := srv.get(id)
	se.mu.Lock()
	n := len(se.buf)
	se.mu.Unlock()
	if n > replayMax {
		t.Fatalf("replay buffer %d bytes, limit %d", n, replayMax)
	}
	// The session still answers.
	_, _ = r.Write([]byte("echo after-flood\r"))
	eventually(t, "after flood", func() bool { return strings.Contains(out.String(), "after-flood") })
}

// Closing a session while its SSH login is still connecting ends it.
func TestKillWhileConnecting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	// A server that accepts TCP and never answers the SSH handshake.
	hang, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hang.Close()
	go func() {
		for {
			c, err := hang.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	dir := t.TempDir()
	st, _ := store.Open(dir)
	srvEntry, err := st.Add(domain.Server{Alias: "hang", Host: "127.0.0.1", Port: hang.Addr().(*net.TCPAddr).Port, User: "u", Password: "p"})
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

	c, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id, err := c.Open(Spec{ServerID: srvEntry.ID}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	out := &sink{}
	r := c.Attach(id, out, nil)
	eventually(t, "connecting message", func() bool { return strings.Contains(out.String(), "Connecting") })
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.List(); len(list) != 0 {
		t.Fatalf("session still listed: %+v", list)
	}
}

// Stopping the service tells attached windows their sessions ended.
func TestStopEndsAttachedSessions(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir, _ := startServer(t)
	c, _ := Dial(dir)
	defer c.Close()
	id, _ := c.Open(Spec{}, 80, 24)
	ended := make(chan struct{})
	c.Attach(id, &sink{}, func() { close(ended) })
	time.Sleep(100 * time.Millisecond)
	other, _ := Dial(dir)
	if err := other.Stop(); err != nil {
		t.Fatal(err)
	}
	other.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("attached window not told about stop")
	}
}
