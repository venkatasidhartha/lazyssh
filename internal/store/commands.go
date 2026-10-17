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

// Command is a saved shell command that can be typed into a terminal pane.
type Command struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Cmd       string    `json:"cmd"`
	UseCount  int       `json:"use_count,omitempty"`
	LastUsed  time.Time `json:"last_used,omitzero"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Label is what the commands bar shows.
func (c Command) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Cmd
}

type commandsFile struct {
	Version  int       `json:"version"`
	Commands []Command `json:"commands"`
}

func (s *Store) commandsPath() string { return filepath.Join(s.dir, "commands.json") }

// Commands returns the saved commands, oldest first.
func (s *Store) Commands() ([]Command, error) {
	data, err := os.ReadFile(s.commandsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f commandsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.commandsPath(), err)
	}
	return f.Commands, nil
}

// mutateCommands applies fn to the latest saved commands under the same
// file lock as the server list, then saves atomically.
func (s *Store) mutateCommands(fn func([]Command) ([]Command, error)) error {
	lock, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	cmds, err := s.Commands()
	if err != nil {
		return err
	}
	cmds, err = fn(cmds)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(commandsFile{Version: 1, Commands: cmds}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "commands-*.json")
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
	return os.Rename(tmp.Name(), s.commandsPath())
}

// AddCommand saves a new command.
func (s *Store) AddCommand(name, cmd string) (Command, error) {
	c := Command{ID: newID(), Name: strings.TrimSpace(name), Cmd: strings.TrimSpace(cmd), CreatedAt: time.Now()}
	if c.Cmd == "" {
		return c, errors.New("command is empty")
	}
	err := s.mutateCommands(func(cmds []Command) ([]Command, error) {
		return append(cmds, c), nil
	})
	return c, err
}

// DeleteCommand removes a saved command.
func (s *Store) DeleteCommand(id string) error {
	return s.mutateCommands(func(cmds []Command) ([]Command, error) {
		out := cmds[:0]
		for _, c := range cmds {
			if c.ID != id {
				out = append(out, c)
			}
		}
		return out, nil
	})
}

// UpdateCommand changes a saved command's name and text.
func (s *Store) UpdateCommand(id, name, cmd string) error {
	name, cmd = strings.TrimSpace(name), strings.TrimSpace(cmd)
	if cmd == "" {
		return errors.New("command is empty")
	}
	return s.mutateCommands(func(cmds []Command) ([]Command, error) {
		for i := range cmds {
			if cmds[i].ID == id {
				cmds[i].Name, cmds[i].Cmd = name, cmd
				return cmds, nil
			}
		}
		return nil, errors.New("command no longer exists")
	})
}

// TouchCommand records that a command was used.
func (s *Store) TouchCommand(id string) error {
	return s.mutateCommands(func(cmds []Command) ([]Command, error) {
		for i := range cmds {
			if cmds[i].ID == id {
				cmds[i].UseCount++
				cmds[i].LastUsed = time.Now()
			}
		}
		return cmds, nil
	})
}
