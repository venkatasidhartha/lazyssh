package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hopssh/internal/domain"
)

func TestRoundTripEncryptsPassword(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	added, err := st.Add(domain.Server{Alias: "a", Host: "10.0.0.1", Password: "s3cret!", Auth: domain.AuthAuto})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(domain.Server{Alias: "A", Host: "10.0.0.2"}); err == nil {
		t.Fatal("duplicate alias (case-insensitive) should be rejected")
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "servers.json"))
	if strings.Contains(string(raw), "s3cret!") {
		t.Fatal("password stored in plain text")
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(added.ID)
	if !ok || got.Password != "s3cret!" || got.Host != "10.0.0.1" {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	if _, err := reopened.Mutate(added.ID, func(s *domain.Server) { s.KeyState = domain.KeyInstalled }); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Delete(added.ID); err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 0 {
		t.Fatal("delete failed")
	}
}
