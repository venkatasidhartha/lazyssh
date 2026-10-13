package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ManagedKey is the ed25519 key lazyssh generates once and installs on
// servers whose first login used a password.
type ManagedKey struct {
	Path   string
	Signer ssh.Signer
	Public string // authorized_keys line
}

// LoadOrCreateManagedKey returns the managed key at dir/id_ed25519,
// generating it on first use.
func LoadOrCreateManagedKey(dir string) (*ManagedKey, error) {
	keyDir := filepath.Join(dir, "keys")
	path := filepath.Join(keyDir, "id_ed25519")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(keyDir, 0o700); err != nil {
			return nil, err
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		host, _ := os.Hostname()
		block, err := ssh.MarshalPrivateKey(priv, "lazyssh@"+host)
		if err != nil {
			return nil, err
		}
		data = pem.EncodeToMemory(block)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
		signer, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			return nil, err
		}
		pub := authorizedLine(signer.PublicKey(), "lazyssh@"+host)
		if err := os.WriteFile(path+".pub", []byte(pub+"\n"), 0o644); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse managed key %s: %w", path, err)
	}
	host, _ := os.Hostname()
	return &ManagedKey{Path: path, Signer: signer, Public: authorizedLine(signer.PublicKey(), "lazyssh@"+host)}, nil
}

func authorizedLine(pub ssh.PublicKey, comment string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + comment
}

// LoadKeyFile parses a user-supplied private key. Passphrase-protected keys
// return ssh.PassphraseMissingError; callers then fall back to the system
// ssh binary, which can prompt for the passphrase.
func LoadKeyFile(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(ExpandHome(path))
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
