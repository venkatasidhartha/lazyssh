package domain

import (
	"fmt"
	"time"
)

// AuthPref is the authentication strategy the user chose for a server.
type AuthPref string

const (
	// AuthAuto logs in with the password the first time, installs a lazyssh
	// managed key and uses that key from then on. If the server refuses the
	// key, it silently stays on password authentication.
	AuthAuto AuthPref = "auto"
	// AuthPassword never tries to install a key.
	AuthPassword AuthPref = "password"
	// AuthKey only uses the configured key (or ssh-agent / default keys).
	AuthKey AuthPref = "key"
)

// KeyStatus records what lazyssh learned about key auth for an AuthAuto server.
type KeyStatus string

const (
	KeyUnknown     KeyStatus = ""            // never connected yet
	KeyInstalled   KeyStatus = "installed"   // managed key installed and verified
	KeyUnsupported KeyStatus = "unsupported" // server does not allow key install/login
)

type Server struct {
	ID       string    `json:"id"`
	Alias    string    `json:"alias"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	User     string    `json:"user"`
	Password string    `json:"-"` // stored encrypted, see store package
	KeyPath  string    `json:"key_path,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
	Auth     AuthPref  `json:"auth"`
	KeyState KeyStatus `json:"key_state,omitempty"`
	KeyNote  string    `json:"key_note,omitempty"` // why key setup failed

	PinnedAt  time.Time `json:"pinned_at,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	SSHCount  int       `json:"ssh_count"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

func (s Server) Addr() string {
	port := s.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s:%d", s.Host, port)
}

func (s Server) Target() string {
	if s.User == "" {
		return s.Host
	}
	return s.User + "@" + s.Host
}

// AuthLabel is a short human description of how the next login will happen.
func (s Server) AuthLabel() string {
	switch s.Auth {
	case AuthPassword:
		return "password only"
	case AuthKey:
		if s.KeyPath != "" {
			return "key (" + s.KeyPath + ")"
		}
		return "key (agent / default keys)"
	}
	switch {
	case s.KeyPath != "":
		return "key (" + s.KeyPath + ")"
	case s.KeyState == KeyInstalled:
		return "key (auto-installed)"
	case s.KeyState == KeyUnsupported:
		return "password (server refused key)"
	case s.Password != "":
		return "password → key setup on first login"
	default:
		return "agent / default keys"
	}
}
