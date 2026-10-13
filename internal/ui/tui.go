// Package ui is the lazyssh terminal interface. Its look and key bindings
// follow the original lazyssh by Adembc (github.com/adembc/lazyssh).
package ui

import (
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
	t.app.EnableMouse(true)
	t.initializeTheme()
	t.buildComponents()
	t.buildLayout()
	t.root.SetInputCapture(t.handleGlobalKeys)
	t.refreshServerList()
	t.app.SetRoot(t.root, true).SetFocus(t.serverList)
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
	t.details = NewServerDetails()
	t.statusBar = NewStatusBar()
}

func (t *App) buildLayout() {
	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(t.searchBar, 3, 0, false).
		AddItem(t.serverList, 0, 1, true)
	content := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(left, 0, 3, true).
		AddItem(t.details, 0, 2, false)
	t.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(t.header, 2, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(t.statusBar, 1, 0, false)
}

func (t *App) serverCount() int { return len(t.store.List()) }

func (t *App) updateListTitle() {
	t.serverList.SetTitle(" Servers — Sort: " + t.sortMode.String() + " ")
}
