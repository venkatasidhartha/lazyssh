package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
	"lazyssh/internal/search"
)

const newTagPrefix = "+ new tag: "

// tagUsage counts how many servers use each tag (case-insensitive, keeping
// the first spelling seen).
func tagUsage(servers []domain.Server) map[string]int {
	counts := map[string]int{}
	canonical := map[string]string{}
	for _, s := range servers {
		for _, t := range s.Tags {
			key := strings.ToLower(t)
			if _, ok := canonical[key]; !ok {
				canonical[key] = t
			}
			counts[canonical[key]]++
		}
	}
	return counts
}

// joinTags formats tags for editing, with a trailing ", " so the next tag
// can be typed straight away.
func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ", ") + ", "
}

// splitTagInput returns the finished tags before the last comma and the
// tag currently being typed.
func splitTagInput(text string) (done []string, current string) {
	parts := strings.Split(text, ",")
	for _, p := range parts[:len(parts)-1] {
		if p = strings.TrimSpace(p); p != "" {
			done = append(done, p)
		}
	}
	return done, strings.TrimSpace(parts[len(parts)-1])
}

// attachSuggestions adds a suggestion list to field. The list opens only
// while typing or when ↓ is pressed — never just because the field got
// focus. Enter or a click picks the highlighted entry; Tab and Shift+Tab
// always move to the next/previous field without picking anything.
func attachSuggestions(field *tview.InputField, suggest func(text string, showAll bool) []string, pick func(label string)) {
	showAll, suppress := false, true // suppress: no list when the form opens

	field.SetAutocompleteStyles(tcell.Color235,
		tcell.StyleDefault.Background(tcell.Color235).Foreground(tcell.Color252),
		tcell.StyleDefault.Background(tcell.Color24).Foreground(tcell.Color255))

	field.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyDown:
			showAll = true
		case tcell.KeyTab, tcell.KeyBacktab:
			// Close the list first so Tab moves on instead of picking.
			suppress = true
			field.Autocomplete()
			suppress = false
		}
		return ev
	})
	field.SetAutocompleteFunc(func(text string) []string {
		all := showAll
		showAll = false
		if suppress {
			return nil
		}
		return suggest(text, all)
	})
	suppress = false
	field.SetAutocompletedFunc(func(label string, _ int, source int) bool {
		switch source {
		case tview.AutocompletedNavigate:
			return false
		case tview.AutocompletedTab:
			return true // never pick on Tab
		}
		pick(label)
		return true
	})
}

// attachTagPicker turns a comma-separated input field into a tag picker:
// typing shows matching existing tags (fuzzy), ↓ lists all of them, Enter
// picks one. A tag that does not exist yet is created by picking
// "+ new tag" or by typing it followed by a comma.
func attachTagPicker(field *tview.InputField, usage map[string]int) {
	labels := map[string]string{} // dropdown label -> tag

	attachSuggestions(field, func(text string, all bool) []string {
		done, current := splitTagInput(text)
		if current == "" && !all {
			return nil
		}
		chosen := map[string]bool{}
		for _, t := range done {
			chosen[strings.ToLower(t)] = true
		}

		type cand struct {
			tag   string
			score int
		}
		var cands []cand
		exact := false
		for tag := range usage {
			if chosen[strings.ToLower(tag)] {
				continue
			}
			if strings.EqualFold(tag, current) {
				exact = true
			}
			if sc, ok := search.Score(current, tag); ok {
				cands = append(cands, cand{tag, sc})
			}
		}
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].score != cands[j].score {
				return cands[i].score > cands[j].score
			}
			return strings.ToLower(cands[i].tag) < strings.ToLower(cands[j].tag)
		})

		clear(labels)
		var entries []string
		for _, c := range cands {
			n := usage[c.tag]
			label := fmt.Sprintf("%s  (%d server%s)", c.tag, n, map[bool]string{true: "", false: "s"}[n == 1])
			labels[label] = c.tag
			entries = append(entries, label)
		}
		// Offer the typed text as a new tag last: Enter picks the best
		// existing match, and typing a comma always keeps the text as is.
		if current != "" && !exact && !chosen[strings.ToLower(current)] {
			label := newTagPrefix + current
			labels[label] = current
			entries = append(entries, label)
		}
		return entries
	}, func(label string) {
		tag, ok := labels[label]
		if !ok {
			return
		}
		done, _ := splitTagInput(field.GetText())
		field.SetText(strings.Join(append(done, tag), ", ") + ", ")
	})
}
