package vterm

import (
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

// StartCommand runs cmd in a new PTY whose output goes to p.
func StartCommand(p *Pane, cmd *exec.Cmd) error {
	cols, rows := p.Size()
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return err
	}
	be := &ptyBackend{f: f, cmd: cmd}
	p.Attach(be)
	go func() {
		_, _ = io.Copy(p.Output(), f)
		_ = cmd.Wait()
		p.Exited()
	}()
	return nil
}

type ptyBackend struct {
	f   *os.File
	cmd *exec.Cmd
}

func (b *ptyBackend) Write(p []byte) (int, error) { return b.f.Write(p) }

func (b *ptyBackend) Resize(cols, rows int) error {
	return pty.Setsize(b.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (b *ptyBackend) Close() error {
	if b.cmd.Process != nil {
		_ = b.cmd.Process.Signal(os.Interrupt)
		_ = b.cmd.Process.Kill()
	}
	return b.f.Close()
}

// StartSSH opens a login shell on client inside p. The client is closed
// when the shell ends.
func StartSSH(p *Pane, client *ssh.Client) error {
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return err
	}
	cols, rows := p.Size()
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		sess.Close()
		client.Close()
		return err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return err
	}
	sess.Stdout, sess.Stderr = p.Output(), p.Output()
	if err := sess.Shell(); err != nil {
		sess.Close()
		client.Close()
		return err
	}
	p.Attach(&sshBackend{client: client, sess: sess, stdin: stdin})
	go func() {
		_ = sess.Wait()
		client.Close()
		p.Exited()
	}()
	return nil
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
