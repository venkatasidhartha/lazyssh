package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
)

// isForwarding reports whether a server has active port forwards.
var isForwarding func(id string) bool

func renderTagBadgesForList(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	const maxTags = 2
	shown := tags
	if len(tags) > maxTags {
		shown = tags[:maxTags]
	}
	parts := make([]string, 0, len(shown)+1)
	for _, t := range shown {
		parts = append(parts, fmt.Sprintf("[black:#5FAFFF] %s [-:-:-]", tview.Escape(t)))
	}
	if extra := len(tags) - len(shown); extra > 0 {
		parts = append(parts, fmt.Sprintf("[#8A8A8A]+%d[-]", extra))
	}
	return strings.Join(parts, " ")
}

// cellPad pads s to width display cells so emoji icons keep alignment.
func cellPad(s string, width int) string {
	w := runewidth.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func pinnedIcon(pinnedAt time.Time) string {
	if pinnedAt.IsZero() {
		return "📡"
	}
	return "📌"
}

func formatServerLine(s domain.Server) string {
	icon := cellPad(pinnedIcon(s.PinnedAt), 2)
	fwd := isForwarding != nil && isForwarding(s.ID)
	fCol := cellPad("", 2)
	if fwd {
		fCol = "[#A0FFA0]" + cellPad("Ⓕ", 2) + "[-]"
	}
	return fmt.Sprintf("%s [white::b]%-12s[-] [#AAAAAA]%-18s[-] %s [#888888]Last SSH: %s[-]  %s",
		icon, tview.Escape(s.Alias), tview.Escape(s.Host), fCol, humanizeDuration(s.LastSeen), renderTagBadgesForList(s.Tags))
}

func humanizeDuration(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", max(1, int(d.Hours())/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", max(1, int(d.Hours())/(24*365)))
	}
}

// SortMode controls how unpinned servers are ordered.
type SortMode int

const (
	SortByAliasAsc SortMode = iota
	SortByAliasDesc
	SortByLastSeenDesc
	SortByLastSeenAsc
)

func (m SortMode) String() string {
	switch m {
	case SortByAliasDesc:
		return "Alias ↓"
	case SortByLastSeenAsc:
		return "Last SSH ↑"
	case SortByLastSeenDesc:
		return "Last SSH ↓"
	default:
		return "Alias ↑"
	}
}

func (m SortMode) ToggleField() SortMode {
	switch m {
	case SortByAliasAsc:
		return SortByLastSeenAsc
	case SortByAliasDesc:
		return SortByLastSeenDesc
	case SortByLastSeenAsc:
		return SortByAliasAsc
	default:
		return SortByAliasDesc
	}
}

func (m SortMode) Reverse() SortMode {
	switch m {
	case SortByAliasAsc:
		return SortByAliasDesc
	case SortByAliasDesc:
		return SortByAliasAsc
	case SortByLastSeenAsc:
		return SortByLastSeenDesc
	default:
		return SortByLastSeenAsc
	}
}

// sortServers puts pinned servers first (newest pin first), then orders the
// rest by mode. Servers never connected to sink to the bottom when sorting
// by last SSH.
func sortServers(servers []domain.Server, mode SortMode) {
	lower := strings.ToLower
	sort.SliceStable(servers, func(i, j int) bool {
		si, sj := servers[i], servers[j]
		pi, pj := !si.PinnedAt.IsZero(), !sj.PinnedAt.IsZero()
		if pi != pj {
			return pi
		}
		if pi {
			if !si.PinnedAt.Equal(sj.PinnedAt) {
				return si.PinnedAt.After(sj.PinnedAt)
			}
			return lower(si.Alias) < lower(sj.Alias)
		}
		switch mode {
		case SortByLastSeenDesc, SortByLastSeenAsc:
			zi, zj := si.LastSeen.IsZero(), sj.LastSeen.IsZero()
			if zi != zj {
				return !zi
			}
			if !zi && !si.LastSeen.Equal(sj.LastSeen) {
				if mode == SortByLastSeenDesc {
					return si.LastSeen.After(sj.LastSeen)
				}
				return si.LastSeen.Before(sj.LastSeen)
			}
			return lower(si.Alias) < lower(sj.Alias)
		case SortByAliasDesc:
			return lower(si.Alias) > lower(sj.Alias)
		default:
			return lower(si.Alias) < lower(sj.Alias)
		}
	})
}
