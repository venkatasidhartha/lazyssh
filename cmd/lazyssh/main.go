// lazyssh is a terminal SSH manager: fuzzy search your servers, press Enter
// to connect. First logins use the password, then an SSH key is installed
// automatically; servers that refuse keys keep using the saved password.
package main

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"lazyssh/internal/domain"
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
			fmt.Println("lazyssh — simple terminal SSH manager\n\nUsage: lazyssh\n\nData directory: ~/.config/lazyssh (override with $LAZYSSH_HOME)")
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
	return ui.New(st, conn, version).Run()
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
