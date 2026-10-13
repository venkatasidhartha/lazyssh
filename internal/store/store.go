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
	"syscall"
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
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

// readDisk returns the servers currently saved on disk (none if the file
// does not exist yet).
func (s *Store) readDisk() ([]domain.Server, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w (the previous version is kept at %s.bak)", s.path, err, s.path)
	}
	servers := make([]domain.Server, 0, len(f.Servers))
	for _, r := range f.Servers {
		srv := r.Server
		pw, err := s.box.open(r.PasswordEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt password for %q: %w", srv.Alias, err)
		}
		srv.Password = pw
		servers = append(servers, srv)
	}
	return servers, nil
}

// Reload re-reads the file, picking up changes made by another lazyssh
// running at the same time. On error the current list is kept.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	servers, err := s.readDisk()
	if err != nil {
		return err
	}
	s.servers = servers
	return nil
}

// transact runs fn on the latest saved list while holding an exclusive
// file lock, then saves. Re-reading first means two lazyssh windows never
// overwrite each other's changes. If fn or the save fails, nothing changes.
func (s *Store) transact(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	latest, err := s.readDisk()
	if err != nil {
		return err // never overwrite a file we could not read
	}
	s.servers = cloneServers(latest)
	if err := fn(); err != nil {
		s.servers = latest
		return err
	}
	if err := s.save(); err != nil {
		s.servers = latest
		return err
	}
	return nil
}

func cloneServers(in []domain.Server) []domain.Server {
	out := make([]domain.Server, len(in))
	for i, srv := range in {
		srv.Tags = append([]string(nil), srv.Tags...)
		out[i] = srv
	}
	return out
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
	return cloneServers(s.servers)
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
	err := s.transact(func() error {
		if s.aliasTaken(srv.Alias, "") {
			return fmt.Errorf("alias %q already exists", srv.Alias)
		}
		srv.ID = newID()
		now := time.Now()
		srv.CreatedAt, srv.UpdatedAt = now, now
		s.servers = append(s.servers, srv)
		return nil
	})
	return srv, err
}

// Update replaces the server with the same ID.
func (s *Store) Update(srv domain.Server) error {
	return s.transact(func() error {
		i := s.indexOf(srv.ID)
		if i < 0 {
			return fmt.Errorf("server %q no longer exists (deleted in another window?)", srv.Alias)
		}
		if s.aliasTaken(srv.Alias, srv.ID) {
			return fmt.Errorf("alias %q already exists", srv.Alias)
		}
		srv.UpdatedAt = time.Now()
		s.servers[i] = srv
		return nil
	})
}

// Mutate applies fn to the stored server with the given ID and saves.
func (s *Store) Mutate(id string, fn func(*domain.Server)) (domain.Server, error) {
	var out domain.Server
	err := s.transact(func() error {
		i := s.indexOf(id)
		if i < 0 {
			return errors.New("server not found (deleted in another window?)")
		}
		fn(&s.servers[i])
		out = s.servers[i]
		return nil
	})
	return out, err
}

func (s *Store) Delete(id string) error {
	return s.transact(func() error {
		if i := s.indexOf(id); i >= 0 {
			s.servers = append(s.servers[:i], s.servers[i+1:]...)
		}
		return nil
	})
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
