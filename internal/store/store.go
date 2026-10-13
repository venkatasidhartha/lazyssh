// Package store persists servers in a JSON file under the user config dir.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lazyssh/internal/domain"
)

const fileVersion = 1

type record struct {
	domain.Server
	PasswordEnc string `json:"password_enc,omitempty"`
}

type fileFormat struct {
	Version int      `json:"version"`
	Servers []record `json:"servers"`
}

type Store struct {
	mu      sync.Mutex
	dir     string
	path    string
	box     *secretBox
	servers []domain.Server
}

// Dir returns the lazyssh data directory (~/.config/lazyssh by default,
// overridable with $LAZYSSH_HOME).
func Dir() (string, error) {
	if d := os.Getenv("LAZYSSH_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "lazyssh")
	// The project was called hopssh before; carry its data over once.
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		old := filepath.Join(base, "hopssh")
		if _, err := os.Stat(old); err == nil {
			_ = os.Rename(old, dir)
		}
	}
	return dir, nil
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	box, err := loadOrCreateSecret(filepath.Join(dir, "secret.key"))
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, path: filepath.Join(dir, "servers.json"), box: box}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w (a backup is kept at %s.bak)", s.path, err, s.path)
	}
	for _, r := range f.Servers {
		srv := r.Server
		pw, err := s.box.open(r.PasswordEnc)
		if err != nil {
			return fmt.Errorf("decrypt password for %q: %w", srv.Alias, err)
		}
		srv.Password = pw
		s.servers = append(s.servers, srv)
	}
	return nil
}

// save writes atomically (temp file + rename) and keeps the previous file
// as servers.json.bak.
func (s *Store) save() error {
	f := fileFormat{Version: fileVersion, Servers: make([]record, 0, len(s.servers))}
	for _, srv := range s.servers {
		enc, err := s.box.seal(srv.Password)
		if err != nil {
			return err
		}
		f.Servers = append(f.Servers, record{Server: srv, PasswordEnc: enc})
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "servers-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if old, err := os.ReadFile(s.path); err == nil {
		_ = os.WriteFile(s.path+".bak", old, 0o600)
	}
	return os.Rename(tmp.Name(), s.path)
}

func (s *Store) List() []domain.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Server, len(s.servers))
	for i, srv := range s.servers {
		srv.Tags = append([]string(nil), srv.Tags...)
		out[i] = srv
	}
	return out
}

func (s *Store) Get(id string) (domain.Server, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.indexOf(id); i >= 0 {
		return s.servers[i], true
	}
	return domain.Server{}, false
}

func (s *Store) indexOf(id string) int {
	for i := range s.servers {
		if s.servers[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) aliasTaken(alias, exceptID string) bool {
	for _, srv := range s.servers {
		if srv.ID != exceptID && strings.EqualFold(srv.Alias, alias) {
			return true
		}
	}
	return false
}

// Add stores a new server and returns it with its generated ID.
func (s *Store) Add(srv domain.Server) (domain.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aliasTaken(srv.Alias, "") {
		return srv, fmt.Errorf("alias %q already exists", srv.Alias)
	}
	srv.ID = newID()
	now := time.Now()
	srv.CreatedAt, srv.UpdatedAt = now, now
	s.servers = append(s.servers, srv)
	if err := s.save(); err != nil {
		s.servers = s.servers[:len(s.servers)-1]
		return srv, err
	}
	return srv, nil
}

// Update replaces the server with the same ID.
func (s *Store) Update(srv domain.Server) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(srv.ID)
	if i < 0 {
		return fmt.Errorf("server %q not found", srv.Alias)
	}
	if s.aliasTaken(srv.Alias, srv.ID) {
		return fmt.Errorf("alias %q already exists", srv.Alias)
	}
	prev := s.servers[i]
	srv.UpdatedAt = time.Now()
	s.servers[i] = srv
	if err := s.save(); err != nil {
		s.servers[i] = prev
		return err
	}
	return nil
}

// Mutate applies fn to the stored server with the given ID and saves.
func (s *Store) Mutate(id string, fn func(*domain.Server)) (domain.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(id)
	if i < 0 {
		return domain.Server{}, errors.New("server not found")
	}
	prev := s.servers[i]
	fn(&s.servers[i])
	if err := s.save(); err != nil {
		s.servers[i] = prev
		return prev, err
	}
	return s.servers[i], nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(id)
	if i < 0 {
		return nil
	}
	prev := append([]domain.Server(nil), s.servers...)
	s.servers = append(s.servers[:i], s.servers[i+1:]...)
	if err := s.save(); err != nil {
		s.servers = prev
		return err
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
