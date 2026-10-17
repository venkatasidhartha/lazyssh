package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
)

// Author is shown in the header bar.
const Author = "Venkata Sidhartha K"

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

type AppHeader struct {
	*tview.Flex
	right    *tview.TextView
	count    func() int
	onButton func(id string)
}

// OnButton is called with "term", "groups" or "quit" when a header button
// is clicked.
func (h *AppHeader) OnButton(f func(id string)) *AppHeader { h.onButton = f; return h }

func NewAppHeader(version string, count func() int) *AppHeader {
	bg := tcell.Color234
	h := &AppHeader{Flex: tview.NewFlex(), count: count}

	left := tview.NewTextView().SetDynamicColors(true).SetRegions(true).SetTextAlign(tview.AlignLeft)
	left.SetBackgroundColor(bg)
	left.SetText(`🚀 [#FFFFFF::b]lazy[-][#55D7FF::b]ssh[-:-:-]  ["term"][black:#55D7FF] ▶ Terminal [-:-][""] ["groups"][black:#87D7AF] ▣ Groups [-:-][""] ["quit"][black:#FF8787] ⏻ Quit [-:-][""]`)
	left.SetMouseCapture(func(a tview.MouseAction, ev *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if a == tview.MouseLeftDoubleClick {
			a = tview.MouseLeftClick
		}
		return a, ev
	})
	left.SetHighlightedFunc(func(added, _, _ []string) {
		if len(added) > 0 && h.onButton != nil {
			left.Highlight()
			h.onButton(added[0])
		}
	})

	center := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	center.SetBackgroundColor(bg)
	center.SetText(makeTag(version, "#22C55E") + "  [#AAAAAA]by[-] [#FFFFFF::b]" + Author + "[-:-:-]")

	h.right = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)
	h.right.SetBackgroundColor(bg)
	h.right.SetDrawFunc(func(_ tcell.Screen, x, y, w, ht int) (int, int, int, int) {
		h.right.SetText(fmt.Sprintf("[#55AAFF]%d servers[-]  [#AAAAAA]• %s[-] ",
			h.count(), time.Now().Format("Mon, 02 Jan 2006 15:04:05 MST")))
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
	return "[white]↑↓[-] Navigate  • [white]Enter[-] SSH  • [white]l[-] Local terminal  • [white]w[-] Workspace  • [white]o[-] Open in pane  • [white]f[-] Forward  • [white]x[-] Stop Forward  • [white]c[-] Copy SSH  • [white]a[-] Add  • [white]e[-] Edit  • [white]g[-] Ping  • [white]d[-] Delete  • [white]p[-] Pin/Unpin  • [white]/[-] Search  • [white]q[-] Quit"
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
// Server list (table)
// ---------------------------------------------------------------------------

var serverColumns = []string{"", "NAME", "HOST / IP", "USER", "AUTH", "LAST SSH", "TAGS"}

// ServerList shows servers as a table with a fixed header row. Row i+1
// holds servers[i]. Column widths come from the content, so long names
// cannot push other columns out of line.
type ServerList struct {
	*tview.Table
	servers           []domain.Server
	onSelectionChange func(domain.Server)
	onReturnToSearch  func()
	onActivate        func(domain.Server)
}

func NewServerList() *ServerList {
	sl := &ServerList{Table: tview.NewTable()}
	sl.Table.SetBorder(true).
		SetTitle(" Servers ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.Color238).
		SetTitleColor(tcell.Color250)
	sl.Table.SetFixed(1, 0).
		SetSelectable(true, false).
		SetSeparator(' ').
		SetSelectedStyle(tcell.StyleDefault.Background(tcell.Color24).Foreground(tcell.Color255).Bold(true))

	sl.Table.SetSelectionChangedFunc(func(row, _ int) {
		if row < 1 && len(sl.servers) > 0 {
			sl.Table.Select(1, 0) // never select the header
			return
		}
		if i := row - 1; i >= 0 && i < len(sl.servers) && sl.onSelectionChange != nil {
			sl.onSelectionChange(sl.servers[i])
		}
	})
	sl.Table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
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

func headerCell(text string) *tview.TableCell {
	return tview.NewTableCell(text).
		SetTextColor(tcell.Color245).
		SetAttributes(tcell.AttrBold).
		SetSelectable(false).
		SetExpansion(0)
}

// UpdateServers replaces the rows, keeping the selection on selectID when
// it is still present.
func (sl *ServerList) UpdateServers(servers []domain.Server, selectID string) {
	sl.servers = servers
	sl.Table.Clear()
	for c, h := range serverColumns {
		sl.Table.SetCell(0, c, headerCell(h))
	}
	for i, s := range servers {
		row := i + 1
		for c, cell := range serverRowCells(s) {
			// Clicking the already-selected server connects to it.
			cell.SetClickedFunc(func() bool {
				if cur, _ := sl.Table.GetSelection(); cur == row && sl.onActivate != nil {
					sl.onActivate(sl.servers[row-1])
					return true
				}
				return false
			})
			sl.Table.SetCell(row, c, cell)
		}
	}
	// The last column takes the remaining width.
	sl.Table.GetCell(0, len(serverColumns)-1).SetExpansion(1)

	idx := 0
	for i := range servers {
		if servers[i].ID == selectID {
			idx = i
			break
		}
	}
	if len(servers) > 0 {
		sl.Table.Select(idx+1, 0)
		if sl.onSelectionChange != nil {
			sl.onSelectionChange(servers[idx])
		}
	}
}

func (sl *ServerList) GetSelectedServer() (domain.Server, bool) {
	i := sl.GetCurrentItem()
	if i >= 0 && i < len(sl.servers) {
		return sl.servers[i], true
	}
	return domain.Server{}, false
}

// GetCurrentItem returns the selected server index (not table row).
func (sl *ServerList) GetCurrentItem() int {
	row, _ := sl.Table.GetSelection()
	return row - 1
}

func (sl *ServerList) SetCurrentItem(i int) {
	if i >= 0 && i < len(sl.servers) {
		sl.Table.Select(i+1, 0)
	}
}

func (sl *ServerList) GetItemCount() int { return len(sl.servers) }

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
	onEye      func() // the clickable "show"/"hide" next to the password
	managedKey string // path of the key lazyssh installs on servers
}

func NewServerDetails() *ServerDetails {
	sd := &ServerDetails{TextView: tview.NewTextView()}
	sd.TextView.SetRegions(true)
	sd.TextView.SetHighlightedFunc(func(added, _, _ []string) {
		for _, r := range added {
			if r == "eye" && sd.onEye != nil {
				sd.onEye()
			}
		}
		sd.TextView.Highlight() // regions act as buttons, not selections
	})
	sd.TextView.SetDynamicColors(true).
		SetWrap(true).
		SetBorder(true).
		SetTitle(" Details ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.Color238).
		SetTitleColor(tcell.Color250)
	return sd
}

// SetManagedKeyPath sets the path shown for servers using the managed key.
func (sd *ServerDetails) SetManagedKeyPath(p string) *ServerDetails {
	sd.managedKey = p
	return sd
}

// tildePath shortens a path under the home directory to ~/...
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// OnEye sets what happens when the password show/hide button is clicked.
func (sd *ServerDetails) OnEye(fn func()) *ServerDetails { sd.onEye = fn; return sd }

// UpdateServer shows s. With revealPassword the saved password is shown in
// clear text instead of dots.
func (sd *ServerDetails) UpdateServer(s domain.Server, forwards []string, revealPassword bool) {
	lastSeen := "Never"
	if !s.LastSeen.IsZero() {
		lastSeen = s.LastSeen.Format("2006-01-02 15:04:05")
	}
	port := s.Port
	if port == 0 {
		port = 22
	}
	key := "[#888888]none[-]"
	switch {
	case s.KeyPath != "":
		key = "[white]" + tview.Escape(s.KeyPath) + "[-]"
	case s.KeyState == domain.KeyInstalled && sd.managedKey != "":
		key = "[white]" + tview.Escape(tildePath(sd.managedKey)) + "[-] [#888888](managed)[-]"
	}
	pw := "[#888888]not set[-]"
	switch {
	case s.Password != "" && revealPassword:
		pw = "[#FFD75F]" + tview.Escape(s.Password) + "[-]  " + eyeButton("hide")
	case s.Password != "":
		pw = "[white]••••••••[-]  " + eyeButton("show")
	}

	text := fmt.Sprintf(
		"[::b]%s[-]\n\n[::b]Basic Settings:[-]\n  Host: [white]%s[-]\n  User: [white]%s[-]\n  Port: [white]%d[-]\n  Key:  %s\n  Password: %s\n  Tags: %s\n  Pinned: [white]%t[-]\n  Last SSH: %s\n  SSH Count: [white]%d[-]\n",
		tview.Escape(s.Alias), tview.Escape(s.Host), tview.Escape(s.User), port,
		key, pw, renderTagChips(s.Tags), !s.PinnedAt.IsZero(), lastSeen, s.SSHCount)

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

	text += "\n[::b]Commands:[-]\n  Enter: SSH connect\n  f: Port forward\n  x: Stop forwarding\n  c: Copy SSH command\n  g: Ping server\n  r: Refresh list\n  a: Add new server\n  e: Edit entry\n  t: Edit tags\n  d: Delete entry\n  p: Pin/Unpin\n  s/S: Sort / reverse\n  v: Show/hide password\n  y: Copy password\n  K: Redo key setup"
	sd.TextView.SetText(text)
	sd.TextView.ScrollToBeginning()
}

// eyeButton is the clickable [show]/[hide] next to the password. It uses a
// plain colour only: underline/bold attributes leaked into later lines.
func eyeButton(label string) string {
	return `["eye"][#55D7FF]` + tview.Escape("["+label+"]") + `[-][""]`
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
