package sessions

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Client is lazyssh's connection to the session service.
type Client struct {
	conn net.Conn

	wmu sync.Mutex
	enc *gob.Encoder

	mu       sync.Mutex
	seq      int
	pending  map[int]chan Msg
	attached map[string]*Remote
	lost     bool
}

// Dial connects to a running service.
func Dial(dir string) (*Client, error) {
	conn, err := net.DialTimeout("unix", SocketPath(dir), 2*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, enc: gob.NewEncoder(conn), pending: map[int]chan Msg{}, attached: map[string]*Remote{}}
	go c.read()
	m, err := c.call(Msg{Kind: "hello", Version: Version})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if m.Version != Version {
		conn.Close()
		return nil, fmt.Errorf("the running session service is a different version (%d, want %d)", m.Version, Version)
	}
	return c, nil
}

// Ensure connects to the service, starting it first if it is not running.
// exe is the lazyssh binary; it is started as "exe __sessiond".
func Ensure(dir, exe string) (*Client, error) {
	if c, err := Dial(dir); err == nil {
		return c, nil
	}
	logf, err := os.OpenFile(filepath.Join(dir, "sessiond.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "__sessiond")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlives this terminal
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	var last error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		c, err := Dial(dir)
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, fmt.Errorf("session service did not start: %v", last)
}

// Lost reports whether the connection to the service is gone.
func (c *Client) Lost() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lost
}

// Close disconnects; the sessions keep running.
func (c *Client) Close() { c.conn.Close() }

func (c *Client) send(m Msg) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.enc.Encode(m)
}

func (c *Client) call(m Msg) (Msg, error) {
	c.mu.Lock()
	if c.lost {
		c.mu.Unlock()
		return Msg{}, errors.New("session service connection lost")
	}
	c.seq++
	m.Seq = c.seq
	ch := make(chan Msg, 1)
	c.pending[m.Seq] = ch
	c.mu.Unlock()
	if err := c.send(m); err != nil {
		return Msg{}, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return Msg{}, errors.New("session service connection lost")
		}
		if r.Err != "" {
			return r, errors.New(r.Err)
		}
		return r, nil
	case <-time.After(10 * time.Second):
		return Msg{}, errors.New("session service did not answer")
	}
}

func (c *Client) read() {
	dec := gob.NewDecoder(c.conn)
	for {
		var m Msg
		if err := dec.Decode(&m); err != nil {
			break
		}
		if m.Seq != 0 {
			c.mu.Lock()
			ch := c.pending[m.Seq]
			delete(c.pending, m.Seq)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		c.mu.Lock()
		r := c.attached[m.ID]
		c.mu.Unlock()
		if r == nil {
			continue
		}
		switch m.Kind {
		case "output":
			_, _ = r.out.Write(m.Data)
		case "exited":
			r.ended()
		}
	}
	// The service is gone: every attached session has ended for us.
	c.mu.Lock()
	c.lost = true
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = map[int]chan Msg{}
	list := make([]*Remote, 0, len(c.attached))
	for _, r := range c.attached {
		list = append(list, r)
	}
	c.mu.Unlock()
	for _, r := range list {
		_, _ = io.WriteString(r.out, "\r\n\x1b[38;5;203m✗ lost the connection to the session service\x1b[0m\r\n")
		r.ended()
	}
}

// List returns the sessions the service is running.
func (c *Client) List() ([]Info, error) {
	m, err := c.call(Msg{Kind: "list"})
	return m.List, err
}

// Open starts a session and returns its id.
func (c *Client) Open(spec Spec, cols, rows int) (string, error) {
	m, err := c.call(Msg{Kind: "open", Spec: spec, Cols: cols, Rows: rows})
	return m.ID, err
}

// Attach streams the session's output (everything so far, then live) to
// out. onExit is called once when the session ends.
func (c *Client) Attach(id string, out io.Writer, onExit func()) *Remote {
	r := &Remote{c: c, id: id, out: out, onExit: onExit}
	c.mu.Lock()
	c.attached[id] = r
	c.mu.Unlock()
	if err := c.send(Msg{Kind: "attach", ID: id}); err != nil {
		r.ended()
	}
	return r
}

// Stop ends every session and stops the service.
func (c *Client) Stop() error {
	_, err := c.call(Msg{Kind: "stop"})
	return err
}

// Rename changes a session's label.
func (c *Client) Rename(id, label string) {
	_ = c.send(Msg{Kind: "rename", ID: id, Spec: Spec{Label: label}})
}

// SaveLayout keeps the workspace layout in the service.
func (c *Client) SaveLayout(data []byte) {
	_ = c.send(Msg{Kind: "layout-save", Data: data})
}

// Layout returns the layout saved by SaveLayout, if any.
func (c *Client) Layout() ([]byte, error) {
	m, err := c.call(Msg{Kind: "layout-get"})
	return m.Data, err
}

// Remote is an attached session, used as a pane's program.
type Remote struct {
	c      *Client
	id     string
	out    io.Writer
	onExit func()
	once   sync.Once
}

func (r *Remote) ID() string { return r.id }

func (r *Remote) ended() {
	r.once.Do(func() {
		if r.onExit != nil {
			r.onExit()
		}
	})
}

func (r *Remote) Write(b []byte) (int, error) {
	if err := r.c.send(Msg{Kind: "input", ID: r.id, Data: b}); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (r *Remote) Resize(cols, rows int) error {
	return r.c.send(Msg{Kind: "resize", ID: r.id, Cols: cols, Rows: rows})
}

// Close ends the session for good.
func (r *Remote) Close() error {
	r.c.mu.Lock()
	delete(r.c.attached, r.id)
	r.c.mu.Unlock()
	_, err := r.c.call(Msg{Kind: "kill", ID: r.id})
	return err
}
