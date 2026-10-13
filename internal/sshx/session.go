package sshx

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// interactive runs a login shell over client in the local terminal and
// closes the client when the shell exits. Used for password-only servers
// (no sshpass needed).
func (c *Connector) interactive(client *ssh.Client) error {
	defer client.Close()

	stdinFd := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFd) {
		return errors.New("stdin is not a terminal")
	}
	// Read keystrokes from our own /dev/tty handle: it is pollable, so the
	// reader goroutine can be stopped with a deadline once the shell exits
	// instead of swallowing the next key meant for the TUI.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tty.Close()

	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	w, h, err := term.GetSize(stdinFd)
	if err != nil {
		w, h = 80, 24
	}
	termType := os.Getenv("TERM")
	if termType == "" {
		termType = "xterm-256color"
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty(termType, h, w, modes); err != nil {
		return fmt.Errorf("request pty: %w", err)
	}
	sess.Stdout, sess.Stderr = os.Stdout, os.Stderr
	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}

	state, err := term.MakeRaw(stdinFd)
	if err != nil {
		return err
	}
	defer term.Restore(stdinFd, state)

	if err := sess.Shell(); err != nil {
		return err
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				if _, werr := stdin.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	done := make(chan struct{})
	defer close(done)
	go func() {
		keepalive := time.NewTicker(30 * time.Second)
		defer keepalive.Stop()
		for {
			select {
			case <-done:
				return
			case <-winch:
				if w, h, err := term.GetSize(stdinFd); err == nil {
					_ = sess.WindowChange(h, w)
				}
			case <-keepalive.C:
				_, _, _ = client.SendRequest("keepalive@openssh.com", true, nil)
			}
		}
	}()

	err = sess.Wait()
	_ = tty.SetReadDeadline(time.Now())

	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return nil // shell's own exit status
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return errors.New("connection closed by remote host")
	}
	return err
}
