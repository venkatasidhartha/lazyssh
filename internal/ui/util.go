package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
)

// isForwarding reports whether a server has active port forwards.
var isForwarding func(id string) bool

// serverRowCells renders one server as table cells, matching serverColumns.
func serverRowCells(s domain.Server) []*tview.TableCell {
	cell := func(text string, color tcell.Color) *tview.TableCell {
		return tview.NewTableCell(tview.Escape(text)).SetTextColor(color).SetMaxWidth(32)
	}
	mark := ""
	markColor := tcell.NewHexColor(0xA0FFA0)
	if !s.PinnedAt.IsZero() {
		mark = "📌"
	}
	if isForwarding != nil && isForwarding(s.ID) {
		mark += "F" // active port forward
	}
	authText, authColor := authShort(s)
	user := s.User
	if user == "" {
		user = "-"
	}
	host := s.Host
	if p := portOf(s); p != 22 {
		host += ":" + strconv.Itoa(p)
	}
	lastColor := tcell.Color250
	if s.LastSeen.IsZero() {
		lastColor = tcell.Color242
	}
	return []*tview.TableCell{
		cell(mark, markColor),
		cell(s.Alias, tcell.ColorWhite).SetAttributes(tcell.AttrBold).SetMaxWidth(20),
		cell(host, tcell.Color252).SetMaxWidth(26),
		cell(user, tcell.Color250).SetMaxWidth(12),
		cell(authText, authColor),
		cell(humanizeDuration(s.LastSeen), lastColor),
		cell(strings.Join(s.Tags, ", "), tcell.NewHexColor(0x5FAFFF)).SetMaxWidth(0),
	}
}

// authShort is the AUTH column: how the next login will happen.
func authShort(s domain.Server) (string, tcell.Color) {
	green, yellow, blue := tcell.NewHexColor(0xA0FFA0), tcell.NewHexColor(0xFFD75F), tcell.NewHexColor(0x87D7FF)
	switch {
	case s.Auth == domain.AuthPassword:
		return "password", yellow
	case s.Auth == domain.AuthKey, s.KeyPath != "":
		return "key", green
	case s.KeyState == domain.KeyInstalled:
		return "key (auto)", green
	case s.KeyState == domain.KeyUnsupported:
		return "password", yellow
	case s.Password != "":
		return "pending", blue
	default:
		return "agent", blue
	}
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
