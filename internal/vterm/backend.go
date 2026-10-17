package vterm

import (
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

// Proc is a running program in a terminal: write its input, resize its
// terminal, end it. It does not need a Pane, so sessions can also run in
// the background service.
type Proc interface {
	io.Writer
	Resize(cols, rows int) error
	Close() error
}

// StartPTY runs cmd in a new PTY. Its output goes to out; onExit is called
// once the program has ended and all output was written.
func StartPTY(cmd *exec.Cmd, cols, rows int, out io.Writer, onExit func()) (Proc, error) {
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(max(1, cols)), Rows: uint16(max(1, rows))})
	if err != nil {
		return nil, err
	}
	go func() {
		_, _ = io.Copy(out, f)
		_ = cmd.Wait()
		onExit()
	}()
	return &ptyBackend{f: f, cmd: cmd}, nil
}

// StartShell opens a login shell on client. The client is closed when the
// shell ends.
func StartShell(client *ssh.Client, cols, rows int, out io.Writer, onExit func()) (Proc, error) {
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", max(1, rows), max(1, cols), modes); err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	sess.Stdout, sess.Stderr = out, out
	if err := sess.Shell(); err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	go func() {
		_ = sess.Wait()
		client.Close()
		onExit()
	}()
	return &sshBackend{client: client, sess: sess, stdin: stdin}, nil
}

// StartCommand runs cmd in a new PTY shown in p.
func StartCommand(p *Pane, cmd *exec.Cmd) error {
	cols, rows := p.Size()
	proc, err := StartPTY(cmd, cols, rows, p.Output(), p.Exited)
	if err != nil {
		return err
	}
	p.Attach(proc)
	return nil
}

// StartSSH opens a login shell on client shown in p.
func StartSSH(p *Pane, client *ssh.Client) error {
	cols, rows := p.Size()
	proc, err := StartShell(client, cols, rows, p.Output(), p.Exited)
	if err != nil {
		return err
	}
	p.Attach(proc)
	return nil
}

type ptyBackend struct {
	f   *os.File
	cmd *exec.Cmd

	mu     sync.Mutex // a resize must not use the file while it is closed
	closed bool
}

func (b *ptyBackend) Write(p []byte) (int, error) { return b.f.Write(p) }

func (b *ptyBackend) Resize(cols, rows int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return os.ErrClosed
	}
	return pty.Setsize(b.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (b *ptyBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if b.cmd.Process != nil {
		_ = b.cmd.Process.Signal(os.Interrupt)
		_ = b.cmd.Process.Kill()
	}
	return b.f.Close()
}

type sshBackend struct {
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
}

func (b *sshBackend) Write(p []byte) (int, error) { return b.stdin.Write(p) }

func (b *sshBackend) Resize(cols, rows int) error { return b.sess.WindowChange(rows, cols) }

func (b *sshBackend) Close() error {
	_ = b.sess.Close()
	return b.client.Close()
}
