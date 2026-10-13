package store

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"lazyssh/internal/domain"
)

func mustOpen(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func aliases(servers []domain.Server) map[string]bool {
	m := map[string]bool{}
	for _, s := range servers {
		m[s.Alias] = true
	}
	return m
}

// Two lazyssh windows open at once must not overwrite each other.
func TestTwoWindowsDoNotOverwriteEachOther(t *testing.T) {
	dir := t.TempDir()
	a, b := mustOpen(t, dir), mustOpen(t, dir)

	x, err := a.Add(domain.Server{Alias: "from-a", Host: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Add(domain.Server{Alias: "from-b", Host: "10.0.0.2"}); err != nil {
		t.Fatal(err)
	}
	// Window A still has its old in-memory list; pinning must not drop from-b.
	if _, err := a.Mutate(x.ID, func(s *domain.Server) { s.PinnedAt = time.Now() }); err != nil {
		t.Fatal(err)
	}
	disk := mustOpen(t, dir).List()
	if got := aliases(disk); !got["from-a"] || !got["from-b"] || len(disk) != 2 {
		t.Fatalf("on disk: %v", got)
	}
	// Duplicate alias is detected against the other window's data too.
	if _, err := a.Add(domain.Server{Alias: "FROM-B", Host: "10.0.0.3"}); err == nil {
		t.Fatal("duplicate alias from another window was accepted")
	}
	// Refresh (Reload) shows the other window's server.
	if err := b.Reload(); err != nil || len(b.List()) != 2 {
		t.Fatalf("reload: %v %d", err, len(b.List()))
	}
	// Editing a server deleted in another window fails cleanly.
	if err := b.Delete(x.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Mutate(x.ID, func(s *domain.Server) { s.SSHCount++ }); err == nil {
		t.Fatal("mutating a deleted server should fail")
	}
}

func TestParallelWritersLoseNothing(t *testing.T) {
	dir := t.TempDir()
	const writers, each = 8, 15
	var wg sync.WaitGroup
	for w := range writers {
		st := mustOpen(t, dir)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				if _, err := st.Add(domain.Server{Alias: fmt.Sprintf("w%d-%d", w, i), Host: "h"}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if n := len(mustOpen(t, dir).List()); n != writers*each {
		t.Fatalf("got %d servers, want %d", n, writers*each)
	}
}

func TestCorruptFileIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	st := mustOpen(t, dir)
	srv, _ := st.Add(domain.Server{Alias: "a", Host: "h"})
	_, _ = st.Mutate(srv.ID, func(s *domain.Server) { s.SSHCount++ }) // 2nd save creates .bak

	path := filepath.Join(dir, "servers.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("Open should fail on a corrupt file")
	}
	if err := st.Reload(); err == nil {
		t.Fatal("Reload should fail on a corrupt file")
	}
	if len(st.List()) != 1 {
		t.Fatal("failed reload must keep the current list")
	}
	if _, err := st.Mutate(srv.ID, func(s *domain.Server) { s.SSHCount++ }); err == nil {
		t.Fatal("write should be refused while the file is corrupt")
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Fatalf("corrupt file was overwritten: %q", b)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatal("backup missing")
	}
}

func TestPermissionsAndBackup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lazyssh")
	st := mustOpen(t, dir)
	srv, _ := st.Add(domain.Server{Alias: "a", Host: "h", Password: "pw"})
	_, _ = st.Mutate(srv.ID, func(s *domain.Server) { s.SSHCount = 7 })

	for path, want := range map[string]os.FileMode{
		dir:                                    0o700,
		filepath.Join(dir, "servers.json"):     0o600,
		filepath.Join(dir, "servers.json.bak"): 0o600,
		filepath.Join(dir, "secret.key"):       0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", filepath.Base(path), got, want)
		}
	}
	// .bak holds the previous version (SSHCount 0), main file the new one.
	bak, _ := os.ReadFile(filepath.Join(dir, "servers.json.bak"))
	main, _ := os.ReadFile(filepath.Join(dir, "servers.json"))
	if string(bak) == string(main) {
		t.Fatal("backup should be the previous version")
	}
	// No temp files left behind.
	if m, _ := filepath.Glob(filepath.Join(dir, "servers-*.json")); len(m) > 0 {
		t.Fatalf("temp files left: %v", m)
	}
}

func TestEveryFieldSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	in := domain.Server{
		Alias: "db", Host: "10.1.2.3", Port: 2222, User: "postgres", Password: "p@ss'w\"rd ü",
		KeyPath: "~/.ssh/id_rsa", Tags: []string{"prod", "db"}, Auth: domain.AuthPassword,
		KeyState: domain.KeyUnsupported, KeyNote: "server does not accept public keys",
		PinnedAt: now, LastSeen: now, SSHCount: 42,
	}
	added, err := mustOpen(t, dir).Add(in)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := mustOpen(t, dir).Get(added.ID)
	if !ok {
		t.Fatal("not found after restart")
	}
	in.ID, in.CreatedAt, in.UpdatedAt = added.ID, got.CreatedAt, got.UpdatedAt
	got.PinnedAt, got.LastSeen = got.PinnedAt.Truncate(time.Second), got.LastSeen.Truncate(time.Second)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}
