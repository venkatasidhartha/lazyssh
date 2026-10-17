// Package sessions runs terminal sessions (local shells and SSH logins) in a
// background service, so they keep running when lazyssh is closed and come
// back when it is opened again.
package sessions

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"lazyssh/internal/domain"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
	"lazyssh/internal/vterm"
)

// Spec says what a session runs.
type Spec struct {
	Label    string // name shown in lazyssh
	ServerID string // saved server to log in to; empty for a local shell
	Cwd      string // working directory of a local shell
}

// Start runs spec: a local shell, or an SSH login to the saved server with
// the built-in client (password or key), falling back to the system ssh.
// Progress messages and the program's output go to out; onExit is called
// when it ends.
func Start(st *store.Store, conn sshx.Connector, spec Spec, cols, rows int, out io.Writer, onExit func()) (vterm.Proc, error) {
	if spec.ServerID == "" {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd := exec.Command(shell)
		if fi, err := os.Stat(spec.Cwd); err == nil && fi.IsDir() {
			cmd.Dir = spec.Cwd
		}
		return vterm.StartPTY(cmd, cols, rows, out, onExit)
	}

	s, ok := st.Get(spec.ServerID)
	if !ok {
		_ = st.Reload() // added in another window?
		if s, ok = st.Get(spec.ServerID); !ok {
			return nil, errors.New("this server is no longer saved in lazyssh")
		}
	}
	fmt.Fprintf(out, "\x1b[38;5;81m›\x1b[0m Connecting to %s…\r\n", s.Target())
	conn.Out = out
	conn.PromptPassword = nil
	touch := func() {
		_, _ = st.Mutate(s.ID, func(x *domain.Server) {
			x.LastSeen = time.Now()
			x.SSHCount++
		})
	}
	if s.Password != "" || s.KeyPath != "" || s.KeyState == domain.KeyInstalled {
		client, err := conn.DialAuto(s)
		if err == nil {
			var proc vterm.Proc
			if proc, err = vterm.StartShell(client, cols, rows, out, onExit); err == nil {
				touch()
				return proc, nil
			}
		}
		fmt.Fprintf(out, "\x1b[38;5;214m!\x1b[0m %v\r\n", err)
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, errors.New("no ssh client installed")
	}
	key := s.KeyPath
	if key == "" && s.KeyState == domain.KeyInstalled && conn.Key != nil {
		key = conn.Key.Path
	}
	proc, err := vterm.StartPTY(exec.Command("ssh", sshx.SSHArgs(s, key)...), cols, rows, out, onExit)
	if err == nil {
		touch()
	}
	return proc, err
}
