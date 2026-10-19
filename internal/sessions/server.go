package sessions

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
	"lazyssh/internal/vterm"
)

// Version is bumped when the protocol changes incompatibly.
const Version = 1

// Msg is every message between lazyssh and the service, both ways.
type Msg struct {
	Seq     int // matches a reply to its request; 0 for events
	Kind    string
	ID      string
	Data    []byte
	Cols    int
	Rows    int
	Spec    Spec
	List    []Info
	Err     string
	Version int
}

// Info describes a running (or ended) session.
type Info struct {
	ID      string
	Spec    Spec
	Exited  bool
	Started time.Time
}

// SocketPath is where the service listens: inside lazyssh's data
// directory, or, when that path is too long for a unix socket (about 108
// bytes), in the user's private runtime directory.
func SocketPath(dir string) string {
	p := filepath.Join(dir, "sessions.sock")
	if len(p) < 100 {
		return p
	}
	sum := sha256.Sum256([]byte(dir))
	name := "lazyssh-" + hex.EncodeToString(sum[:6]) + ".sock"
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" && len(rt) < 60 {
		return filepath.Join(rt, name)
	}
	private := filepath.Join(os.TempDir(), fmt.Sprintf("lazyssh-%d", os.Getuid()))
	_ = os.MkdirAll(private, 0o700)
	return filepath.Join(private, name)
}

// replayMax is how much output each session keeps for reattaching.
const replayMax = 2 << 20

// Server owns the sessions.
type Server struct {
	st   *store.Store
	conn sshx.Connector

	mu       sync.Mutex
	sessions map[string]*session
	clients  map[*peer]bool
	layout   []byte
	lastBusy time.Time
	stopped  chan struct{} // closed by a "stop" request
	stopOnce sync.Once
}

func NewServer(st *store.Store, conn sshx.Connector) *Server {
	return &Server{st: st, conn: conn, sessions: map[string]*session{}, clients: map[*peer]bool{}, lastBusy: time.Now(), stopped: make(chan struct{})}
}

// Listen opens the service socket, replacing a stale one. It fails if a
// service is already running.
func Listen(dir string) (net.Listener, error) {
	path := SocketPath(dir)
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, errors.New("the session service is already running")
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve handles connections until ln is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

// Idle reports whether nothing has used the service for d.
func (s *Server) Idle(d time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) > 0 || len(s.clients) > 0 {
		s.lastBusy = time.Now()
		return false
	}
	return time.Since(s.lastBusy) > d
}

// Disconnect drops every connected window (as when the process dies).
func (s *Server) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := range s.clients {
		p.conn.Close()
	}
}

// Shutdown ends every session.
func (s *Server) Shutdown() {
	s.mu.Lock()
	list := make([]*session, 0, len(s.sessions))
	for _, se := range s.sessions {
		list = append(list, se)
	}
	s.mu.Unlock()
	for _, se := range list {
		se.kill()
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

type session struct {
	id      string
	started time.Time

	mu         sync.Mutex
	spec       Spec
	proc       vterm.Proc
	buf        []byte
	alt        bool // the program is on the alternate screen (htop, vim…)
	exited     bool
	killed     bool
	cols, rows int
	subs       map[*peer]bool
	early      []byte // typed while the program was still starting
}

// Write takes the program's output: kept for reattaching and sent to
// everyone attached.
func (se *session) Write(b []byte) (int, error) {
	se.mu.Lock()
	defer se.mu.Unlock()
	se.buf = append(se.buf, b...)
	if len(se.buf) > replayMax {
		se.buf = append([]byte(nil), se.buf[len(se.buf)-replayMax*3/4:]...)
	}
	trackAltScreen(&se.alt, b)
	data := append([]byte(nil), b...)
	for p := range se.subs {
		p.send(Msg{Kind: "output", ID: se.id, Data: data})
	}
	return len(b), nil
}

var (
	altOn  = [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1047h"), []byte("\x1b[?47h")}
	altOff = [][]byte{[]byte("\x1b[?1049l"), []byte("\x1b[?1047l"), []byte("\x1b[?47l")}
)

// trackAltScreen follows switches to and from the alternate screen.
func trackAltScreen(alt *bool, b []byte) {
	on, off := -1, -1
	for _, s := range altOn {
		on = max(on, bytes.LastIndex(b, s))
	}
	for _, s := range altOff {
		off = max(off, bytes.LastIndex(b, s))
	}
	if on > off {
		*alt = true
	} else if off > on {
		*alt = false
	}
}

func (se *session) exit() {
	se.mu.Lock()
	defer se.mu.Unlock()
	if se.exited {
		return
	}
	se.exited = true
	for p := range se.subs {
		p.send(Msg{Kind: "exited", ID: se.id})
	}
}

func (se *session) kill() {
	se.mu.Lock()
	proc := se.proc
	se.killed = true
	se.mu.Unlock()
	if proc != nil {
		_ = proc.Close()
	}
	se.exit()
}

func (se *session) info() Info {
	se.mu.Lock()
	defer se.mu.Unlock()
	return Info{ID: se.id, Spec: se.spec, Exited: se.exited, Started: se.started}
}

func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) open(spec Spec, cols, rows int) *session {
	se := &session{id: newSessionID(), started: time.Now(), spec: spec, cols: max(cols, 20), rows: max(rows, 5), subs: map[*peer]bool{}}
	s.mu.Lock()
	s.sessions[se.id] = se
	s.mu.Unlock()
	cols0, rows0 := se.cols, se.rows // read before another goroutine can resize
	go func() {
		proc, err := Start(s.st, s.conn, spec, cols0, rows0, se, se.exit)
		if err != nil {
			fmt.Fprintf(se, "\x1b[38;5;203m✗ %v\x1b[0m\r\n", err)
			se.exit()
			return
		}
		se.mu.Lock()
		se.proc = proc
		killed, cols, rows, early := se.killed, se.cols, se.rows, se.early
		se.early = nil
		se.mu.Unlock()
		if killed {
			_ = proc.Close()
			return
		}
		_ = proc.Resize(cols, rows) // it may have been resized while connecting
		if len(early) > 0 {
			_, _ = proc.Write(early)
		}
	}()
	return se
}

func (s *Server) get(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------

// peer is one connected lazyssh window. Messages to it are queued so a slow
// window never holds up a session.
type peer struct {
	conn net.Conn
	out  chan Msg
	done chan struct{}
	once sync.Once
}

func (p *peer) send(m Msg) {
	select {
	case p.out <- m:
	case <-p.done:
	default:
		p.close() // far behind: drop it, it reattaches when it reconnects
	}
}

func (p *peer) close() { p.once.Do(func() { close(p.done) }) }

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	p := &peer{conn: c, out: make(chan Msg, 4096), done: make(chan struct{})}
	s.mu.Lock()
	s.clients[p] = true
	s.mu.Unlock()
	defer func() {
		p.close()
		s.mu.Lock()
		delete(s.clients, p)
		list := make([]*session, 0, len(s.sessions))
		for _, se := range s.sessions {
			list = append(list, se)
		}
		s.mu.Unlock()
		for _, se := range list {
			se.mu.Lock()
			delete(se.subs, p)
			se.mu.Unlock()
		}
	}()
	go func() {
		enc := gob.NewEncoder(c)
		for {
			select {
			case m := <-p.out:
				if enc.Encode(m) != nil {
					p.close()
					c.Close()
					return
				}
			case <-p.done:
				c.Close()
				return
			}
		}
	}()

	dec := gob.NewDecoder(c)
	for {
		var m Msg
		if err := dec.Decode(&m); err != nil {
			return
		}
		reply := Msg{Seq: m.Seq, Kind: m.Kind, ID: m.ID}
		switch m.Kind {
		case "hello":
			reply.Version = Version
		case "list":
			s.mu.Lock()
			for _, se := range s.sessions {
				reply.List = append(reply.List, se.info())
			}
			s.mu.Unlock()
		case "open":
			reply.ID = s.open(m.Spec, m.Cols, m.Rows).id
		case "attach":
			se := s.get(m.ID)
			if se == nil {
				// Gone (closed elsewhere, or the service restarted): say so
				// and end it, the window would otherwise wait forever.
				p.send(Msg{Kind: "output", ID: m.ID, Data: []byte("\x1b[38;5;214m!\x1b[0m this session no longer exists\r\n")})
				p.send(Msg{Kind: "exited", ID: m.ID})
				continue
			}
			se.mu.Lock()
			// Everything so far, then live output, in order.
			p.send(Msg{Kind: "output", ID: se.id, Data: append([]byte(nil), se.buf...)})
			if se.exited {
				p.send(Msg{Kind: "exited", ID: se.id})
			}
			se.subs[p] = true
			proc, alt, exited, cols, rows := se.proc, se.alt, se.exited, se.cols, se.rows
			se.mu.Unlock()
			if proc != nil && alt && !exited {
				// Make a full-screen program draw itself again.
				go func() {
					_ = proc.Resize(cols, max(1, rows-1))
					time.Sleep(50 * time.Millisecond)
					_ = proc.Resize(cols, rows)
				}()
			}
			continue // the replay was the reply
		case "detach":
			if se := s.get(m.ID); se != nil {
				se.mu.Lock()
				delete(se.subs, p)
				se.mu.Unlock()
			}
			continue
		case "input":
			if se := s.get(m.ID); se != nil {
				se.mu.Lock()
				proc := se.proc
				if proc == nil && !se.exited && len(se.early) < 1<<16 {
					se.early = append(se.early, m.Data...)
				}
				se.mu.Unlock()
				if proc != nil {
					_, _ = proc.Write(m.Data)
				}
			}
			continue
		case "resize":
			if se := s.get(m.ID); se != nil && m.Cols > 0 && m.Rows > 0 {
				se.mu.Lock()
				se.cols, se.rows = m.Cols, m.Rows
				proc := se.proc
				se.mu.Unlock()
				if proc != nil {
					_ = proc.Resize(m.Cols, m.Rows)
				}
			}
			continue
		case "rename":
			if se := s.get(m.ID); se != nil {
				se.mu.Lock()
				se.spec.Label = m.Spec.Label
				se.mu.Unlock()
			}
			continue
		case "kill":
			if se := s.get(m.ID); se != nil {
				se.kill()
				s.mu.Lock()
				delete(s.sessions, m.ID)
				s.mu.Unlock()
			}
		case "layout-save":
			s.mu.Lock()
			s.layout = m.Data
			s.mu.Unlock()
			continue
		case "stop":
			s.Shutdown()
			s.mu.Lock()
			s.sessions = map[string]*session{}
			s.layout = nil
			s.mu.Unlock()
			s.stopOnce.Do(func() { close(s.stopped) })
		case "layout-get":
			s.mu.Lock()
			reply.Data = s.layout
			s.mu.Unlock()
		default:
			reply.Err = "unknown request " + m.Kind
		}
		if m.Seq != 0 {
			p.send(reply)
		}
	}
}
