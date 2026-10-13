package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var knownHostsMu sync.Mutex

func knownHostsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "known_hosts")
}

// hostKeyCallback verifies against ~/.ssh/known_hosts (shared with the
// system ssh client). Unknown hosts are trusted on first use and recorded,
// like `StrictHostKeyChecking=accept-new`. A changed key is rejected.
func hostKeyCallback(log func(string, ...any)) (ssh.HostKeyCallback, error) {
	path := knownHostsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			knownHostsMu.Lock()
			defer knownHostsMu.Unlock()
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
			f, ferr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if ferr != nil {
				return ferr
			}
			defer f.Close()
			if _, ferr := f.WriteString(line + "\n"); ferr != nil {
				return ferr
			}
			if log != nil {
				log("Added %s (%s) to known_hosts, fingerprint %s", hostname, key.Type(), ssh.FingerprintSHA256(key))
			}
			return nil
		}
		if errors.As(err, &ke) {
			return fmt.Errorf("HOST KEY CHANGED for %s (got %s %s). Possible man-in-the-middle attack; "+
				"if the server was reinstalled remove the old entry with: ssh-keygen -R %s",
				hostname, key.Type(), ssh.FingerprintSHA256(key), knownhosts.Normalize(hostname))
		}
		return err
	}, nil
}

// knownHostKeyAlgorithms returns the key types already recorded for addr so
// the handshake negotiates a key we can verify (avoids false "key changed"
// errors when the server has several host keys).
func knownHostKeyAlgorithms(addr string) []string {
	cb, err := knownhosts.New(knownHostsPath())
	if err != nil {
		return nil
	}
	tcp, _ := net.ResolveTCPAddr("tcp", addr)
	if tcp == nil {
		tcp = &net.TCPAddr{IP: net.IPv4zero}
	}
	err = cb(addr, tcp, probeKey{})
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) {
		return nil
	}
	var algos []string
	seen := map[string]bool{}
	for _, k := range ke.Want {
		t := k.Key.Type()
		var expanded []string
		switch t {
		case ssh.KeyAlgoRSA:
			expanded = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		default:
			expanded = []string{t}
		}
		for _, a := range expanded {
			if !seen[a] {
				seen[a] = true
				algos = append(algos, a)
			}
		}
	}
	return algos
}

// probeKey is a key type that can never be in known_hosts; checking it
// makes knownhosts report every key it does know for the host.
type probeKey struct{}

func (probeKey) Type() string                        { return "lazyssh-probe" }
func (probeKey) Marshal() []byte                     { return []byte("lazyssh-probe") }
func (probeKey) Verify([]byte, *ssh.Signature) error { return errors.New("probe") }
