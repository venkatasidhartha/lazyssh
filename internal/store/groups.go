package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Group is a saved set of workspace sessions (which servers or local
// shells, their names and how they are laid out) that can be opened again
// in one go. Layout is the workspace's own format.
type Group struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Layout    json.RawMessage `json:"layout"`
	Sessions  []string        `json:"sessions,omitempty"` // names, for showing
	CreatedAt time.Time       `json:"created_at,omitzero"`
	LastUsed  time.Time       `json:"last_used,omitzero"`
}

type groupsFile struct {
	Version int     `json:"version"`
	Groups  []Group `json:"groups"`
}

func (s *Store) groupsPath() string { return filepath.Join(s.dir, "groups.json") }

// Groups returns the saved session groups, oldest first.
func (s *Store) Groups() ([]Group, error) {
	data, err := os.ReadFile(s.groupsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f groupsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.groupsPath(), err)
	}
	return f.Groups, nil
}

func (s *Store) mutateGroups(fn func([]Group) ([]Group, error)) error {
	lock, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	groups, err := s.Groups()
	if err != nil {
		return err
	}
	if groups, err = fn(groups); err != nil {
		return err
	}
	data, err := json.MarshalIndent(groupsFile{Version: 1, Groups: groups}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "groups-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.groupsPath())
}

// SaveGroup stores a group under name, replacing a group with that name.
func (s *Store) SaveGroup(name string, layout json.RawMessage, sessions []string) (Group, error) {
	g := Group{ID: newID(), Name: strings.TrimSpace(name), Layout: layout, Sessions: sessions, CreatedAt: time.Now()}
	if g.Name == "" {
		return g, errors.New("give the group a name")
	}
	err := s.mutateGroups(func(groups []Group) ([]Group, error) {
		for i := range groups {
			if strings.EqualFold(groups[i].Name, g.Name) {
				g.ID, g.CreatedAt = groups[i].ID, groups[i].CreatedAt
				groups[i] = g
				return groups, nil
			}
		}
		return append(groups, g), nil
	})
	return g, err
}

// RenameGroup changes a group's name.
func (s *Store) RenameGroup(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("give the group a name")
	}
	return s.mutateGroups(func(groups []Group) ([]Group, error) {
		for i := range groups {
			if groups[i].ID == id {
				groups[i].Name = name
				return groups, nil
			}
		}
		return nil, errors.New("group no longer exists")
	})
}

// DeleteGroup removes a group (its sessions are not touched).
func (s *Store) DeleteGroup(id string) error {
	return s.mutateGroups(func(groups []Group) ([]Group, error) {
		out := groups[:0]
		for _, g := range groups {
			if g.ID != id {
				out = append(out, g)
			}
		}
		return out, nil
	})
}

// TouchGroup records that a group was opened.
func (s *Store) TouchGroup(id string) error {
	return s.mutateGroups(func(groups []Group) ([]Group, error) {
		for i := range groups {
			if groups[i].ID == id {
				groups[i].LastUsed = time.Now()
			}
		}
		return groups, nil
	})
}
