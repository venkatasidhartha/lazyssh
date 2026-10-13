package ui

import (
	"reflect"
	"testing"
	"time"

	"lazyssh/internal/domain"
)

func TestSortModes(t *testing.T) {
	now := time.Now()
	base := []domain.Server{
		{Alias: "charlie", LastSeen: now.Add(-1 * time.Hour)},
		{Alias: "alpha", LastSeen: now.Add(-3 * time.Hour)},
		{Alias: "Bravo"}, // never connected
		{Alias: "pinned-old", PinnedAt: now.Add(-time.Hour)},
		{Alias: "delta", LastSeen: now.Add(-2 * time.Hour)},
		{Alias: "pinned-new", PinnedAt: now},
	}
	cases := map[SortMode][]string{
		// pinned always first (newest pin first); case-insensitive alias order
		SortByAliasAsc:  {"pinned-new", "pinned-old", "alpha", "Bravo", "charlie", "delta"},
		SortByAliasDesc: {"pinned-new", "pinned-old", "delta", "charlie", "Bravo", "alpha"},
		// "never" stays at the bottom in both directions
		SortByLastSeenDesc: {"pinned-new", "pinned-old", "charlie", "delta", "alpha", "Bravo"},
		SortByLastSeenAsc:  {"pinned-new", "pinned-old", "alpha", "delta", "charlie", "Bravo"},
	}
	for mode, want := range cases {
		servers := append([]domain.Server(nil), base...)
		sortServers(servers, mode)
		var got []string
		for _, s := range servers {
			got = append(got, s.Alias)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", mode, got, want)
		}
	}
}

func TestSortKeysCycle(t *testing.T) {
	// s switches field and keeps direction; S reverses direction.
	if SortByAliasAsc.ToggleField() != SortByLastSeenAsc || SortByLastSeenAsc.ToggleField() != SortByAliasAsc {
		t.Fatal("s should toggle Alias <-> Last SSH")
	}
	if SortByAliasAsc.Reverse() != SortByAliasDesc || SortByLastSeenDesc.Reverse() != SortByLastSeenAsc {
		t.Fatal("S should reverse")
	}
}
