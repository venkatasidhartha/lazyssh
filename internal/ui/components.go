package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
)

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

type AppHeader struct {
	*tview.Flex
	right *tview.TextView
	count func() int
}

func NewAppHeader(version string, count func() int) *AppHeader {
	bg := tcell.Color234
	h := &AppHeader{Flex: tview.NewFlex(), count: count}

	left := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
	left.SetBackgroundColor(bg)
	left.SetText("🚀 [#FFFFFF::b]lazy[-][#55D7FF::b]ssh[-]")

	center := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	center.SetBackgroundColor(bg)
	center.SetText(makeTag(version, "#22C55E"))

	h.right = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)
	h.right.SetBackgroundColor(bg)
	h.right.SetDrawFunc(func(_ tcell.Screen, x, y, w, ht int) (int, int, int, int) {
		h.right.SetText(fmt.Sprintf("[#55AAFF]%d servers[-]  [#AAAAAA]• %s[-] ",
			h.count(), time.Now().Format("Mon, 02 Jan 2006 15:04")))
		return x, y, w, ht
	})

	bar := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(left, 0, 1, false).
		AddItem(center, 0, 1, false).
		AddItem(h.right, 0, 1, false)

	sep := tview.NewTextView().SetDynamicColors(true)
	sep.SetBackgroundColor(tcell.Color235)
	sep.SetText("[#444444]" + strings.Repeat("─", 400) + "[-]")

	h.Flex.SetDirection(tview.FlexRow).
		AddItem(bar, 1, 0, false).
		AddItem(sep, 1, 0, false)
	return h
}

func makeTag(text, bg string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "[black:" + bg + "::b]  " + text + "  [-:-:-]"
}

// ---------------------------------------------------------------------------
// Status bar
// ---------------------------------------------------------------------------

func DefaultStatusText() string {
	return "[white]↑↓[-] Navigate  • [white]Enter[-] SSH  • [white]f[-] Forward  • [white]x[-] Stop Forward  • [white]c[-] Copy SSH  • [white]a[-] Add  • [white]e[-] Edit  • [white]g[-] Ping  • [white]d[-] Delete  • [white]p[-] Pin/Unpin  • [white]/[-] Search  • [white]q[-] Quit"
}

func NewStatusBar() *tview.TextView {
	status := tview.NewTextView().SetDynamicColors(true)
	status.SetBackgroundColor(tcell.Color235)
	status.SetTextAlign(tview.AlignCenter)
	status.SetText(DefaultStatusText())
	return status
}

// ---------------------------------------------------------------------------
// Search bar
// ---------------------------------------------------------------------------

type SearchBar struct {
	*tview.InputField
	onSearch   func(string)
	onEscape   func()
	onNavigate func(direction int)
}

func NewSearchBar() *SearchBar {
	s := &SearchBar{InputField: tview.NewInputField()}
	s.InputField.SetLabel(" 🔍 Search: ").
		SetPlaceholder("name, ip or tag…  (press / to search)").
		SetPlaceholderTextColor(tcell.Color242).
		SetFieldBackgroundColor(tcell.Color233).
		SetFieldTextColor(tcell.Color252).
		SetFieldWidth(0).
		SetBorder(true).
		SetTitle(" Search ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.Color238).
		SetTitleColor(tcell.Color250)
	s.InputField.SetChangedFunc(func(text string) {
		if s.onSearch != nil {
			s.onSearch(text)
		}
	})
	s.InputField.SetDoneFunc(func(key tcell.Key) {
		if (key == tcell.KeyEsc || key == tcell.KeyEnter) && s.onEscape != nil {
			s.onEscape()
		}
	})
	s.InputField.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyDown:
			if s.onNavigate != nil {
				s.onNavigate(1)
			}
			return nil
		case tcell.KeyUp:
			if s.onNavigate != nil {
				s.onNavigate(-1)
			}
			return nil
		}
		return event
	})
	return s
}

func (s *SearchBar) OnSearch(fn func(string)) *SearchBar { s.onSearch = fn; return s }
func (s *SearchBar) OnEscape(fn func()) *SearchBar       { s.onEscape = fn; return s }
func (s *SearchBar) OnNavigate(fn func(int)) *SearchBar  { s.onNavigate = fn; return s }

// ---------------------------------------------------------------------------
// Server list
// ---------------------------------------------------------------------------

type ServerList struct {
	*tview.List
	servers           []domain.Server
	onSelectionChange func(domain.Server)
	onReturnToSearch  func()
	onActivate        func(domain.Server)
}

func NewServerList() *ServerList {
	sl := &ServerList{List: tview.NewList()}
	sl.List.ShowSecondaryText(false)
	sl.List.SetBorder(true).
		SetTitle(" Servers ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.Color238).
		SetTitleColor(tcell.Color250)
	sl.List.
		SetSelectedBackgroundColor(tcell.Color24).
		SetSelectedTextColor(tcell.Color255).
		SetHighlightFullLine(true)

	sl.List.SetChangedFunc(func(index int, _, _ string, _ rune) {
		if index >= 0 && index < len(sl.servers) && sl.onSelectionChange != nil {
			sl.onSelectionChange(sl.servers[index])
		}
	})
	// Enter is handled globally, so this only fires on mouse clicks:
	// clicking the already-selected server connects to it.
	sl.List.SetSelectedFunc(func(index int, _, _ string, _ rune) {
		if index == sl.List.GetCurrentItem() && index < len(sl.servers) && sl.onActivate != nil {
			sl.onActivate(sl.servers[index])
		}
	})
	sl.List.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft, tcell.KeyRight, tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyESC:
			if sl.onReturnToSearch != nil {
				sl.onReturnToSearch()
			}
			return nil
		}
		return event
	})
	return sl
}

// UpdateServers replaces the list, keeping the selection on selectID when
// it is still present.
func (sl *ServerList) UpdateServers(servers []domain.Server, selectID string) {
	sl.servers = servers
	sl.List.Clear()
	for i := range servers {
		sl.List.AddItem(formatServerLine(servers[i]), "", 0, nil)
	}
	idx := 0
	for i := range servers {
		if servers[i].ID == selectID {
			idx = i
			break
		}
	}
	if len(servers) > 0 {
		sl.List.SetCurrentItem(idx)
		if sl.onSelectionChange != nil {
			sl.onSelectionChange(servers[idx])
		}
	}
}

func (sl *ServerList) GetSelectedServer() (domain.Server, bool) {
	idx := sl.List.GetCurrentItem()
	if idx >= 0 && idx < len(sl.servers) {
		return sl.servers[idx], true
	}
	return domain.Server{}, false
}

func (sl *ServerList) OnSelectionChange(fn func(domain.Server)) *ServerList {
	sl.onSelectionChange = fn
	return sl
}
func (sl *ServerList) OnReturnToSearch(fn func()) *ServerList { sl.onReturnToSearch = fn; return sl }
func (sl *ServerList) OnActivate(fn func(domain.Server)) *ServerList {
	sl.onActivate = fn
	return sl
}

// ---------------------------------------------------------------------------
// Details panel
// ---------------------------------------------------------------------------

type ServerDetails struct {
	*tview.TextView
}

func NewServerDetails() *ServerDetails {
	sd := &ServerDetails{TextView: tview.NewTextView()}
	sd.TextView.SetDynamicColors(true).
		SetWrap(true).
		SetBorder(true).
		SetTitle(" Details ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.Color238).
		SetTitleColor(tcell.Color250)
	return sd
}

func (sd *ServerDetails) UpdateServer(s domain.Server, forwards []string) {
	lastSeen := "Never"
	if !s.LastSeen.IsZero() {
		lastSeen = s.LastSeen.Format("2006-01-02 15:04:05")
	}
	port := s.Port
	if port == 0 {
		port = 22
	}
	key := s.KeyPath
	if key == "" && s.KeyState == domain.KeyInstalled {
		key = "lazyssh managed key"
	}
	pw := "[#888888]not set[-]"
	if s.Password != "" {
		pw = "[white]•••••••• (encrypted)[-]"
	}

	text := fmt.Sprintf(
		"[::b]%s[-]\n\n[::b]Basic Settings:[-]\n  Host: [white]%s[-]\n  User: [white]%s[-]\n  Port: [white]%d[-]\n  Key:  [white]%s[-]\n  Password: %s\n  Tags: %s\n  Pinned: [white]%t[-]\n  Last SSH: %s\n  SSH Count: [white]%d[-]\n",
		tview.Escape(s.Alias), tview.Escape(s.Host), tview.Escape(s.User), port,
		tview.Escape(key), pw, renderTagChips(s.Tags), !s.PinnedAt.IsZero(), lastSeen, s.SSHCount)

	text += "\n[::b]Authentication:[-]\n  Mode: [white]" + string(s.Auth) + "[-]\n  Login: " + authColor(s) + tview.Escape(s.AuthLabel()) + "[-]\n"
	if s.KeyNote != "" {
		text += "  Note: [#FFAF5F]" + tview.Escape(s.KeyNote) + "[-]\n"
	}
	if len(forwards) > 0 {
		text += "\n[::b]Active Forwards:[-]\n"
		for _, f := range forwards {
			text += "  [#A0FFA0]" + f + "[-]\n"
		}
	}

	text += "\n[::b]Commands:[-]\n  Enter: SSH connect\n  f: Port forward\n  x: Stop forwarding\n  c: Copy SSH command\n  g: Ping server\n  r: Refresh list\n  a: Add new server\n  e: Edit entry\n  t: Edit tags\n  d: Delete entry\n  p: Pin/Unpin\n  s/S: Sort / reverse\n  K: Redo key setup"
	sd.TextView.SetText(text)
	sd.TextView.ScrollToBeginning()
}

func authColor(s domain.Server) string {
	switch {
	case s.Auth == domain.AuthPassword || s.KeyState == domain.KeyUnsupported:
		return "[#FFD75F]"
	case s.KeyState == domain.KeyInstalled || s.KeyPath != "" || s.Auth == domain.AuthKey:
		return "[#A0FFA0]"
	default:
		return "[#87D7FF]"
	}
}

func (sd *ServerDetails) ShowEmpty(msg string) {
	sd.TextView.SetText(msg)
}

func renderTagChips(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	chips := make([]string, 0, len(tags))
	for _, t := range tags {
		chips = append(chips, fmt.Sprintf("[black:#5FAFFF] %s [-:-:-]", tview.Escape(t)))
	}
	return strings.Join(chips, " ")
}
