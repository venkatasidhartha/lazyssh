package search

import (
	"testing"

	"hopssh/internal/domain"
)

func TestFilter(t *testing.T) {
	servers := []domain.Server{
		{Alias: "web-prod-1", Host: "10.0.0.10", Tags: []string{"prod", "web"}},
		{Alias: "db-main", Host: "192.168.1.5", User: "postgres", Tags: []string{"db", "onprem"}},
		{Alias: "cache", Host: "10.0.0.42", Tags: []string{"stg"}},
	}
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"web-prod-1", "db-main", "cache"}},
		{"wp1", []string{"web-prod-1"}},             // fuzzy on alias
		{"192.168", []string{"db-main"}},            // IP
		{"10.0.0", []string{"web-prod-1", "cache"}}, // IP prefix
		{"onprem", []string{"db-main"}},             // tag
		{"#stg", []string{"cache"}},                 // explicit tag
		{"prd web", []string{"web-prod-1"}},         // every term must match
		{"postgres@", []string{"db-main"}},          // user@host
		{"zzz", nil},
	}
	for _, c := range cases {
		got := Filter(servers, c.query)
		var aliases []string
		for _, s := range got {
			aliases = append(aliases, s.Alias)
		}
		if len(aliases) != len(c.want) {
			t.Errorf("Filter(%q) = %v, want %v", c.query, aliases, c.want)
			continue
		}
		for i := range aliases {
			if aliases[i] != c.want[i] {
				t.Errorf("Filter(%q) = %v, want %v", c.query, aliases, c.want)
				break
			}
		}
	}
}

func TestScoreRanksExactHigher(t *testing.T) {
	exact, _ := Score("db", "db")
	fuzzy, _ := Score("db", "dashboard")
	if exact <= fuzzy {
		t.Fatalf("exact %d should beat fuzzy %d", exact, fuzzy)
	}
}
