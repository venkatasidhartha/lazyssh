// lazyssh is a terminal SSH manager: fuzzy search your servers, press Enter
// to connect. First logins use the password, then an SSH key is installed
// automatically; servers that refuse keys keep using the saved password.
package main

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"lazyssh/internal/domain"
	"lazyssh/internal/sessions"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
	"lazyssh/internal/ui"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-v", "--version", "version":
			fmt.Println("lazyssh", version)
			return
		case "-h", "--help", "help":
			fmt.Println("lazyssh " + version + " — simple terminal SSH manager by " + ui.Author + `

Usage:
  lazyssh                  open lazyssh
  lazyssh sessions         list the sessions running in the background
  lazyssh sessions stop    end them all and stop the background service

Workspace sessions keep running in the background after lazyssh closes and
come back when it opens again. Set LAZYSSH_NO_BACKGROUND=1 to end them with
lazyssh instead.

Data directory: ~/.config/lazyssh (override with $LAZYSSH_HOME)`)
			return
		case "__sessiond":
			if err := runSessionService(); err != nil {
				fmt.Fprintln(os.Stderr, "lazyssh session service:", err)
				os.Exit(1)
			}
			return
		case "sessions":
			if err := sessionsCommand(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "lazyssh:", err)
				os.Exit(1)
			}
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lazyssh:", err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := store.Dir()
	if err != nil {
		return err
	}
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	key, err := sshx.LoadOrCreateManagedKey(dir)
	if err != nil {
		return fmt.Errorf("managed key: %w", err)
	}
	conn := &sshx.Connector{
		Key: key,
		Out: os.Stdout,
		Persist: func(s domain.Server) {
			_, _ = st.Mutate(s.ID, func(x *domain.Server) {
				x.KeyState, x.KeyNote = s.KeyState, s.KeyNote
				x.Password = s.Password
			})
		},
		PromptPassword: promptPassword,
	}
	app := ui.New(st, conn, version)
	if os.Getenv("LAZYSSH_NO_BACKGROUND") == "" {
		app.Sessions = func() (*sessions.Client, error) {
			exe, err := os.Executable()
			if err != nil {
				return nil, err
			}
			return sessions.Ensure(dir, exe)
		}
	}
	return app.Run()
}

// runSessionService is the background process that owns the sessions.
func runSessionService() error {
	dir, err := store.Dir()
	if err != nil {
		return err
	}
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	key, err := sshx.LoadOrCreateManagedKey(dir)
	if err != nil {
		return fmt.Errorf("managed key: %w", err)
	}
	conn := sshx.Connector{
		Key: key,
		Persist: func(s domain.Server) {
			_, _ = st.Mutate(s.ID, func(x *domain.Server) {
				x.KeyState, x.KeyNote = s.KeyState, s.KeyNote
				x.Password = s.Password
			})
		},
	}
	return sessions.RunDaemon(dir, st, conn)
}

// sessionsCommand lists or stops the background sessions.
func sessionsCommand(args []string) error {
	dir, err := store.Dir()
	if err != nil {
		return err
	}
	c, err := sessions.Dial(dir)
	if err != nil {
		fmt.Println("No background sessions (the session service is not running).")
		return nil
	}
	defer c.Close()
	if len(args) > 0 && args[0] == "stop" {
		if err := c.Stop(); err != nil {
			return err
		}
		fmt.Println("Ended all background sessions and stopped the session service.")
		return nil
	}
	list, err := c.List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("No background sessions.")
		return nil
	}
	for _, s := range list {
		state := "running"
		if s.Exited {
			state = "ended"
		}
		what := "local shell"
		if s.Spec.ServerID != "" {
			what = "ssh"
		}
		fmt.Printf("%-24s %-12s %-8s since %s\n", s.Spec.Label, what, state, s.Started.Format("Mon 15:04"))
	}
	return nil
}

// promptPassword reads a password from the terminal without echoing it.
func promptPassword(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer tty.Close()
	fmt.Fprint(tty, prompt)
	pw, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	return string(pw), err
}
