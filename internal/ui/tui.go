// Package ui is the lazyssh terminal interface. Its look and key bindings
// follow the original lazyssh by Adembc (github.com/adembc/lazyssh).
package ui

import (
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

type App struct {
	version string

	app       *tview.Application
	store     *store.Store
	connector *sshx.Connector
	forwarder *sshx.Forwarder

	header     *AppHeader
	searchBar  *SearchBar
	serverList *ServerList
	details    *ServerDetails
	statusBar  *tview.TextView
	root       *tview.Flex

	sortMode SortMode

	revealID  string // server whose password is shown in clear text
	revealGen int    // invalidates older auto-hide timers

	suspended atomic.Bool // an SSH session owns the terminal
	statusGen int         // only the newest status message may be cleared

	workspace   *Workspace // split-pane terminal sessions
	inWorkspace bool       // the workspace is on screen
}

func New(st *store.Store, conn *sshx.Connector, version string) *App {
	return &App{
		version:   version,
		app:       tview.NewApplication(),
		store:     st,
		connector: conn,
		forwarder: sshx.NewForwarder(conn),
		sortMode:  SortByAliasAsc,
	}
}

func (t *App) Run() error {
	defer t.forwarder.StopAll()
	defer t.closeAllPanes()
	t.app.EnableMouse(true).EnablePaste(true)
	t.initializeTheme()
	t.buildComponents()
	t.buildLayout()
	t.root.SetInputCapture(t.handleGlobalKeys)
	t.workspace = newWorkspace(t)
	t.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if t.inWorkspace {
			return t.workspace.handleKey(ev)
		}
		return ev
	})
	t.refreshServerList()
	t.app.SetRoot(t.root, true).SetFocus(t.serverList)
	stop := make(chan struct{})
	defer close(stop)
	go t.tick(stop)
	return t.app.Run()
}

func (t *App) initializeTheme() {
	tview.Styles.PrimitiveBackgroundColor = tcell.Color232
	tview.Styles.ContrastBackgroundColor = tcell.Color235
	tview.Styles.BorderColor = tcell.Color238
	tview.Styles.TitleColor = tcell.Color250
	tview.Styles.PrimaryTextColor = tcell.Color252
	tview.Styles.TertiaryTextColor = tcell.Color245
	tview.Styles.SecondaryTextColor = tcell.Color245
	tview.Styles.GraphicsColor = tcell.Color238
}

func (t *App) buildComponents() {
	t.header = NewAppHeader(t.version, t.serverCount)
	t.searchBar = NewSearchBar().
		OnSearch(func(string) { t.refreshServerList() }).
		OnEscape(t.blurSearchBar).
		OnNavigate(t.handleSearchNavigate)
	isForwarding = func(id string) bool { return len(t.forwarder.Active(id)) > 0 }
	t.serverList = NewServerList().
		OnSelectionChange(t.handleServerSelectionChange).
		OnReturnToSearch(t.handleSearchFocus).
		OnActivate(func(domain.Server) { t.handleServerConnect() })
	t.details = NewServerDetails().SetManagedKeyPath(t.connector.Key.Path).OnEye(func() {
		t.handleTogglePassword()
		t.app.SetFocus(t.serverList)
	})
	t.statusBar = NewStatusBar()
}

func (t *App) buildLayout() {
	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(t.searchBar, 3, 0, false).
		AddItem(t.serverList, 0, 1, true)
	// The server table gets all remaining width; the details panel has a
	// fixed width and is hidden on narrow terminals.
	content := tview.NewFlex().SetDirection(tview.FlexColumn)
	content.SetDrawFunc(func(_ tcell.Screen, x, y, w, h int) (int, int, int, int) {
		content.Clear()
		content.AddItem(left, 0, 1, true)
		if w >= 100 {
			content.AddItem(t.details, 44, 0, false)
		}
		return x, y, w, h
	})
	content.AddItem(left, 0, 1, true).AddItem(t.details, 44, 0, false)
	t.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(t.header, 2, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(t.statusBar, 1, 0, false)
}

// tick redraws every second for the header clock and reloads the list
// every 30 seconds so "Last SSH" and changes from other windows show up. It pauses while an SSH
// session has the terminal.
func (t *App) tick(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for n := 1; ; n++ {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		if t.suspended.Load() {
			continue
		}
		refresh := n%30 == 0
		t.app.QueueUpdateDraw(func() {
			// Pick up changes from other lazyssh windows; keep the
			// current list if the file cannot be read.
			if refresh && t.store.Reload() == nil {
				t.refreshServerList()
			}
		})
	}
}

func (t *App) serverCount() int { return len(t.store.List()) }

func (t *App) updateListTitle() {
	t.serverList.SetTitle(" Servers — Sort: " + t.sortMode.String() + " ")
}
