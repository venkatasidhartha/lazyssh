// Package sshx connects to servers: password-first logins, automatic key
// installation, and falling back to password-only for servers that refuse
// keys.
package sshx

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"hopssh/internal/domain"
)

const dialTimeout = 15 * time.Second

type Connector struct {
	Key *ManagedKey
	Out io.Writer
	// Persist saves server state changes (key status, updated password)
	// learned while connecting.
	Persist func(domain.Server)
	// PromptPassword asks the user for a password on the terminal.
	PromptPassword func(prompt string) (string, error)
}

func (c *Connector) logf(format string, args ...any) {
	if c.Out != nil {
		fmt.Fprintf(c.Out, "\x1b[38;5;81m›\x1b[0m "+format+"\r\n", args...)
	}
}

func (c *Connector) warnf(format string, args ...any) {
	if c.Out != nil {
		fmt.Fprintf(c.Out, "\x1b[38;5;214m!\x1b[0m "+format+"\r\n", args...)
	}
}

func (c *Connector) persist(s domain.Server) {
	if c.Persist != nil {
		c.Persist(s)
	}
}

// Connect opens an interactive shell on s, choosing the auth strategy from
// the server's preference and what was learned on earlier logins.
func (c *Connector) Connect(s domain.Server) error {
	switch s.Auth {
	case domain.AuthKey:
		return c.keySession(s, s.KeyPath)
	case domain.AuthPassword:
		if s.Password == "" {
			return c.keySession(s, "") // no password saved: let ssh prompt
		}
		client, _, err := c.dialPassword(s)
		if err != nil {
			return err
		}
		return c.interactive(client)
	}

	// AuthAuto
	if s.KeyPath != "" {
		return c.keySession(s, s.KeyPath)
	}
	if s.KeyState == domain.KeyInstalled {
		err := c.probe(s, ssh.PublicKeys(c.Key.Signer))
		if err == nil {
			return c.keySession(s, c.Key.Path)
		}
		if !isAuthError(err) {
			return err
		}
		c.warnf("The hopssh key is no longer accepted by %s", s.Host)
		s.KeyState, s.KeyNote = domain.KeyUnknown, ""
		c.persist(s)
		if s.Password == "" {
			return errors.New("key login failed and no password is saved; edit the server and add a password")
		}
		c.logf("Falling back to password and re-installing the key")
	}
	if s.Password == "" {
		return c.keySession(s, "")
	}

	c.logf("Logging in to %s with password", s.Target())
	client, s, err := c.dialPassword(s)
	if err != nil {
		return err
	}
	if s.KeyState == domain.KeyUnsupported {
		return c.interactive(client)
	}

	note, err := c.installKey(client, s)
	if err == nil {
		s.KeyState, s.KeyNote = domain.KeyInstalled, ""
		c.persist(s)
		c.logf("\x1b[38;5;120mSSH key installed — next logins will use the key\x1b[0m")
		client.Close()
		return c.keySession(s, c.Key.Path)
	}
	c.warnf("Key setup not possible (%s) — using password login for this server", note)
	s.KeyState, s.KeyNote = domain.KeyUnsupported, note
	c.persist(s)
	return c.interactive(client)
}

// dialPassword logs in with the saved password. If the server rejects it
// (password changed), it asks for the new one up to three times and saves
// it once a login succeeds. Returns s with the working password.
func (c *Connector) dialPassword(s domain.Server) (*ssh.Client, domain.Server, error) {
	client, err := c.dial(s, passwordAuth(s.Password))
	for attempt := 1; isAuthError(err) && attempt <= 3; attempt++ {
		if c.PromptPassword == nil {
			return nil, s, err
		}
		c.warnf("Saved password was rejected by %s", s.Target())
		pw, perr := c.PromptPassword(fmt.Sprintf("New password for %s (empty = cancel): ", s.Target()))
		if perr != nil || pw == "" {
			return nil, s, err
		}
		client, err = c.dial(s, passwordAuth(pw))
		if err == nil {
			s.Password = pw
			c.persist(s)
			c.logf("\x1b[38;5;120mNew password saved\x1b[0m")
		}
	}
	return client, s, err
}

// installKey appends the managed public key to ~/.ssh/authorized_keys and
// verifies a key-only login works. On verification failure the line is
// removed again (if hopssh added it).
func (c *Connector) installKey(client *ssh.Client, s domain.Server) (string, error) {
	c.logf("Installing hopssh key on %s", s.Host)
	k := shellQuote(c.Key.Public)
	install := "umask 077; mkdir -p ~/.ssh && touch ~/.ssh/authorized_keys && chmod 700 ~/.ssh && chmod 600 ~/.ssh/authorized_keys && " +
		"if grep -qxF " + k + " ~/.ssh/authorized_keys; then echo HOPSSH_PRESENT; else printf '%s\\n' " + k + " >> ~/.ssh/authorized_keys && echo HOPSSH_ADDED; fi; " +
		"command -v restorecon >/dev/null 2>&1 && restorecon -R ~/.ssh >/dev/null 2>&1; true"
	out, err := run(client, install)
	added := strings.Contains(out, "HOPSSH_ADDED")
	if !added && !strings.Contains(out, "HOPSSH_PRESENT") {
		msg := firstLine(out)
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "remote shell does not support key install"
		}
		return msg, errors.New(msg)
	}
	if err := c.probe(s, ssh.PublicKeys(c.Key.Signer)); err != nil {
		if added {
			_, _ = run(client, "grep -vxF "+k+" ~/.ssh/authorized_keys > ~/.ssh/.hopssh.tmp; cat ~/.ssh/.hopssh.tmp > ~/.ssh/authorized_keys; rm -f ~/.ssh/.hopssh.tmp")
		}
		if isAuthError(err) {
			return "server does not accept public keys", err
		}
		return "verify failed: " + err.Error(), err
	}
	return "", nil
}

// keySession starts the system ssh client (best terminal experience,
// passphrase prompts, agent). keyPath "" means agent / default keys.
func (c *Connector) keySession(s domain.Server, keyPath string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		// No OpenSSH client installed: use the built-in client.
		var auth []ssh.AuthMethod
		if keyPath != "" {
			signer, err := LoadKeyFile(keyPath)
			if err != nil {
				return fmt.Errorf("load key %s: %w", keyPath, err)
			}
			auth = append(auth, ssh.PublicKeys(signer))
		}
		auth = append(auth, agentAuth()...)
		if s.Password != "" {
			auth = append(auth, passwordAuth(s.Password)...)
		}
		client, err := c.dial(s, auth)
		if err != nil {
			return err
		}
		return c.interactive(client)
	}
	args := SSHArgs(s, keyPath)
	c.logf("%s", strings.Join(append([]string{"ssh"}, args...), " "))
	cmd := exec.Command("ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() != 255 {
		return nil // remote command's own exit status
	}
	if err != nil {
		return fmt.Errorf("ssh exited: %w", err)
	}
	return nil
}

// SSHArgs builds the arguments for the system ssh client.
func SSHArgs(s domain.Server, keyPath string) []string {
	var args []string
	if s.Port != 0 && s.Port != 22 {
		args = append(args, "-p", strconv.Itoa(s.Port))
	}
	if keyPath != "" {
		args = append(args, "-i", ExpandHome(keyPath), "-o", "IdentitiesOnly=yes")
	}
	args = append(args, "-o", "StrictHostKeyChecking=accept-new", "-o", "ServerAliveInterval=30", s.Target())
	return args
}

// CommandLine is the copy-pastable ssh command for a server.
func CommandLine(s domain.Server, managedKeyPath string) string {
	key := s.KeyPath
	if key == "" && s.Auth == domain.AuthAuto && s.KeyState == domain.KeyInstalled {
		key = managedKeyPath
	}
	parts := []string{"ssh"}
	if s.Port != 0 && s.Port != 22 {
		parts = append(parts, "-p", strconv.Itoa(s.Port))
	}
	if key != "" {
		parts = append(parts, "-i", shellQuote(key))
	}
	return strings.Join(append(parts, s.Target()), " ")
}

// DialAuto connects non-interactively using every credential known for s
// (managed key, user key, agent, password). Used for port forwarding.
func (c *Connector) DialAuto(s domain.Server) (*ssh.Client, error) {
	var auth []ssh.AuthMethod
	if s.KeyPath != "" {
		if signer, err := LoadKeyFile(s.KeyPath); err == nil {
			auth = append(auth, ssh.PublicKeys(signer))
		}
	}
	if s.KeyState == domain.KeyInstalled && c.Key != nil {
		auth = append(auth, ssh.PublicKeys(c.Key.Signer))
	}
	auth = append(auth, agentAuth()...)
	if s.Password != "" {
		auth = append(auth, passwordAuth(s.Password)...)
	}
	return c.dial(s, auth)
}

func (c *Connector) probe(s domain.Server, auth ssh.AuthMethod) error {
	client, err := c.dial(s, []ssh.AuthMethod{auth})
	if err != nil {
		return err
	}
	return client.Close()
}

func (c *Connector) dial(s domain.Server, auth []ssh.AuthMethod) (*ssh.Client, error) {
	hk, err := hostKeyCallback(c.logf)
	if err != nil {
		return nil, err
	}
	sup, insec := ssh.SupportedAlgorithms(), ssh.InsecureAlgorithms()
	cfg := &ssh.ClientConfig{
		User:            s.User,
		Auth:            auth,
		HostKeyCallback: hk,
		Timeout:         dialTimeout,
		Config: ssh.Config{
			// Old on-prem appliances often only speak legacy algorithms.
			KeyExchanges: append(sup.KeyExchanges, insec.KeyExchanges...),
			Ciphers:      append(sup.Ciphers, insec.Ciphers...),
			MACs:         append(sup.MACs, insec.MACs...),
		},
	}
	if algos := knownHostKeyAlgorithms(s.Addr()); len(algos) > 0 {
		cfg.HostKeyAlgorithms = algos
	} else {
		cfg.HostKeyAlgorithms = append(sup.HostKeys, insec.HostKeys...)
	}
	if cfg.User == "" {
		cfg.User = os.Getenv("USER")
	}
	client, err := ssh.Dial("tcp", s.Addr(), cfg)
	if err != nil {
		msg := err.Error()
		// x/crypto reports a keyboard-interactive rejection (servers without
		// PAM) as "unexpected message type 51"; both mean auth failed.
		if strings.Contains(msg, "unable to authenticate") || strings.Contains(msg, "unexpected message type 51") {
			return nil, fmt.Errorf("%w for %s on %s", ErrAuthFailed, cfg.User, s.Addr())
		}
		return nil, fmt.Errorf("connect %s: %w", s.Addr(), err)
	}
	return client, nil
}

func passwordAuth(pw string) []ssh.AuthMethod {
	return []ssh.AuthMethod{
		ssh.Password(pw),
		// Many servers (PAM) only offer keyboard-interactive for passwords.
		ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = pw
			}
			return answers, nil
		}),
	}
}

func agentAuth() []ssh.AuthMethod {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	return []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(conn).Signers)}
}

// ErrAuthFailed means the server rejected the credentials (as opposed to a
// network or host key problem).
var ErrAuthFailed = errors.New("authentication failed (wrong user, password or key)")

func isAuthError(err error) bool {
	return errors.Is(err, ErrAuthFailed)
}

func run(client *ssh.Client, cmd string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "HOPSSH_") {
			return l
		}
	}
	return ""
}

// Ping checks TCP reachability of the SSH port.
func Ping(s domain.Server) (time.Duration, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", s.Addr(), 5*time.Second)
	if err != nil {
		return 0, err
	}
	conn.Close()
	return time.Since(start).Round(time.Millisecond), nil
}
