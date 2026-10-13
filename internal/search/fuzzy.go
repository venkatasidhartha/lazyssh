// Package search implements fuzzy matching of servers by alias, host/IP,
// user and tags.
package search

import (
	"sort"
	"strings"
	"unicode"

	"lazyssh/internal/domain"
)

// Score returns how well pattern fuzzy-matches text (subsequence match,
// case-insensitive). ok is false when pattern is not a subsequence of text.
// Consecutive runs, word-boundary hits and prefix hits score higher.
func Score(pattern, text string) (score int, ok bool) {
	p := []rune(strings.ToLower(pattern))
	t := []rune(strings.ToLower(text))
	if len(p) == 0 {
		return 0, true
	}
	if strings.Contains(string(t), string(p)) {
		// Exact substring: strongly preferred, more so at the start.
		s := 1000 + 10*len(p)
		if strings.HasPrefix(string(t), string(p)) {
			s += 500
		}
		if string(t) == string(p) {
			s += 500
		}
		return s, true
	}
	pi, run, prev := 0, 0, -2
	for ti := 0; ti < len(t) && pi < len(p); ti++ {
		if t[ti] != p[pi] {
			continue
		}
		score += 10
		if ti == prev+1 {
			run++
			score += 5 * run
		} else {
			run = 0
		}
		if ti == 0 || !unicode.IsLetter(t[ti-1]) && !unicode.IsDigit(t[ti-1]) {
			score += 15
		}
		prev = ti
		pi++
	}
	if pi < len(p) {
		return 0, false
	}
	return score - len(t)/4, true
}

// fields lists the searchable fields of a server. Tags are prefixed with '#'
// so "#prod" also matches.
func fields(s domain.Server) []string {
	f := []string{s.Alias, s.Host, s.User, s.Target()}
	for _, t := range s.Tags {
		f = append(f, t, "#"+t)
	}
	return f
}

// Filter returns the servers matching query, best match first. Each
// whitespace-separated term must match at least one field. An empty query
// returns all servers unchanged.
func Filter(servers []domain.Server, query string) []domain.Server {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return servers
	}
	type hit struct {
		srv   domain.Server
		score int
	}
	var hits []hit
	for _, srv := range servers {
		total, matched := 0, true
		fs := fields(srv)
		for _, term := range terms {
			best, any := 0, false
			for _, f := range fs {
				if sc, ok := Score(term, f); ok && (!any || sc > best) {
					best, any = sc, true
				}
			}
			if !any {
				matched = false
				break
			}
			total += best
		}
		if matched {
			hits = append(hits, hit{srv, total})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]domain.Server, len(hits))
	for i, h := range hits {
		out[i] = h.srv
	}
	return out
}
