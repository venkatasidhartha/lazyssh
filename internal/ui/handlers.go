package ui

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"hopssh/internal/domain"
	"hopssh/internal/search"
	"hopssh/internal/sshx"
)

func (t *App) handleGlobalKeys(event *tcell.EventKey) *tcell.EventKey {
	if t.app.GetFocus() == t.searchBar {
		return event
	}
	switch event.Rune() {
	case 'q':
		t.app.Stop()
	case '/':
		t.handleSearchFocus()
	case 'a':
		t.showServerForm(nil)
	case 'e':
		if s, ok := t.serverList.GetSelectedServer(); ok {
			t.showServerForm(&s)
		}
	case 'd':
		if s, ok := t.serverList.GetSelectedServer(); ok {
			t.showDeleteConfirmModal(s)
		}
	case 'p':
		t.handleServerPin()
	case 's':
		t.sortMode = t.sortMode.ToggleField()
		t.showStatusTemp("Sort: " + t.sortMode.String())
		t.refreshServerList()
	case 'S':
		t.sortMode = t.sortMode.Reverse()
		t.showStatusTemp("Sort: " + t.sortMode.String())
		t.refreshServerList()
	case 'c':
		t.handleCopyCommand()
	case 'g':
		t.handlePingSelected()
	case 'r':
		t.refreshServerList()
		t.showStatusTemp(fmt.Sprintf("Refreshed %d servers", t.serverList.GetItemCount()))
	case 't':
		if s, ok := t.serverList.GetSelectedServer(); ok {
			t.showEditTagsForm(s)
		}
	case 'f':
		if s, ok := t.serverList.GetSelectedServer(); ok {
			t.showPortForwardForm(s)
		}
	case 'x':
		t.handleStopForwarding()
	case 'K':
		t.handleRedoKeySetup()
	case 'j':
		t.moveSelection(1)
	case 'k':
		t.moveSelection(-1)
	default:
		if event.Key() == tcell.KeyEnter {
			t.handleServerConnect()
			return nil
		}
		return event
	}
	return nil
}

func (t *App) moveSelection(dir int) {
	n := t.serverList.GetItemCount()
	if n == 0 {
		return
	}
	t.serverList.SetCurrentItem((t.serverList.GetCurrentItem() + dir + n) % n)
}

func (t *App) handleSearchFocus() { t.app.SetFocus(t.searchBar) }

func (t *App) blurSearchBar() { t.app.SetFocus(t.serverList) }

func (t *App) handleSearchNavigate(dir int) {
	t.app.SetFocus(t.serverList)
	t.moveSelection(dir)
}

func (t *App) handleServerSelectionChange(s domain.Server) {
	var fwds []string
	for _, f := range t.forwarder.Active(s.ID) {
		fwds = append(fwds, f.String())
	}
	t.details.UpdateServer(s, fwds)
}

// refreshServerList reloads from the store, applying the search query
// (fuzzy, best match first) or the sort mode, and keeps the selection.
func (t *App) refreshServerList() {
	selectID := ""
	if s, ok := t.serverList.GetSelectedServer(); ok {
		selectID = s.ID
	}
	all := t.store.List()
	query := strings.TrimSpace(t.searchBar.GetText())
	var servers []domain.Server
	if query == "" {
		servers = all
		sortServers(servers, t.sortMode)
	} else {
		servers = search.Filter(all, query)
	}
	t.updateListTitle()
	t.serverList.UpdateServers(servers, selectID)
	switch {
	case len(all) == 0:
		t.details.ShowEmpty("\n  [::b]Welcome to hopssh![-]\n\n  No servers yet.\n\n  Press [#55D7FF::b]a[-] to add your first server.\n\n  Enter the IP, user and password once —\n  hopssh logs in with the password, installs\n  an SSH key and uses the key from then on.\n  Servers that refuse keys keep using the\n  saved (encrypted) password automatically.")
	case len(servers) == 0:
		t.details.ShowEmpty("No servers match the current filter.")
	}
}

func (t *App) handleServerConnect() {
	s, ok := t.serverList.GetSelectedServer()
	if !ok {
		return
	}
	t.app.Suspend(func() {
		fmt.Print("\x1b[2J\x1b[H")
		fmt.Printf("🚀 \x1b[1mhop\x1b[38;5;81mssh\x1b[0m → \x1b[1m%s\x1b[0m (%s)\n", s.Alias, s.Target())
		err := t.connector.Connect(s)
		if err == nil {
			_, _ = t.store.Mutate(s.ID, func(x *domain.Server) {
				x.LastSeen = time.Now()
				x.SSHCount++
			})
			return
		}
		fmt.Printf("\n\x1b[38;5;203m✗ %v\x1b[0m\n\nPress Enter to return to hopssh…", err)
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	})
	t.refreshServerList()
}

func (t *App) handleServerPin() {
	s, ok := t.serverList.GetSelectedServer()
	if !ok {
		return
	}
	_, err := t.store.Mutate(s.ID, func(x *domain.Server) {
		if x.PinnedAt.IsZero() {
			x.PinnedAt = time.Now()
		} else {
			x.PinnedAt = time.Time{}
		}
	})
	if err != nil {
		t.showStatusError("Pin failed: " + err.Error())
		return
	}
	t.refreshServerList()
}

func (t *App) handleCopyCommand() {
	s, ok := t.serverList.GetSelectedServer()
	if !ok {
		return
	}
	cmd := sshx.CommandLine(s, t.connector.Key.Path)
	if err := clipboard.WriteAll(cmd); err != nil {
		// No xclip/xsel/wl-copy: ask the terminal to copy via OSC 52.
		if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
			fmt.Fprintf(tty, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(cmd)))
			tty.Close()
		}
	}
	t.showStatusTemp("Copied: " + cmd)
}

func (t *App) handlePingSelected() {
	s, ok := t.serverList.GetSelectedServer()
	if !ok {
		return
	}
	t.showStatusTemp(fmt.Sprintf("Pinging %s…", s.Alias))
	go func() {
		dur, err := sshx.Ping(s)
		t.app.QueueUpdateDraw(func() {
			if err != nil {
				t.showStatusError(fmt.Sprintf("Ping %s: DOWN (%v)", s.Alias, err))
				return
			}
			t.showStatusTemp(fmt.Sprintf("Ping %s: UP (%s)", s.Alias, dur))
		})
	}()
}

func (t *App) handleRedoKeySetup() {
	s, ok := t.serverList.GetSelectedServer()
	if !ok {
		return
	}
	if s.Auth != domain.AuthAuto || s.Password == "" {
		t.showStatusError("Key setup needs Auth = auto and a saved password")
		return
	}
	if _, err := t.store.Mutate(s.ID, func(x *domain.Server) {
		x.KeyState, x.KeyNote = domain.KeyUnknown, ""
	}); err != nil {
		t.showStatusError(err.Error())
		return
	}
	t.refreshServerList()
	t.showStatusTemp("Key setup will run again on next connect")
}

func (t *App) handleStopForwarding() {
	s, ok := t.serverList.GetSelectedServer()
	if !ok {
		return
	}
	if n := t.forwarder.Stop(s.ID); n > 0 {
		t.showStatusTemp(fmt.Sprintf("Stopped %d forward(s) for %s", n, s.Alias))
	} else {
		t.showStatusTemp("No active forwards for " + s.Alias)
	}
	t.refreshServerList()
}

// ---------------------------------------------------------------------------
// Modals and small forms
// ---------------------------------------------------------------------------

func (t *App) showDeleteConfirmModal(s domain.Server) {
	doDelete := func() {
		t.forwarder.Stop(s.ID)
		if err := t.store.Delete(s.ID); err != nil {
			t.returnToMain()
			t.showStatusError("Delete failed: " + err.Error())
			return
		}
		t.returnToMain()
		t.refreshServerList()
		t.showStatusTemp("Deleted " + s.Alias)
	}
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Delete server %s (%s:%d)?\n\nThis action cannot be undone.", s.Alias, s.Target(), portOf(s))).
		AddButtons([]string{"[yellow]C[-]ancel", "[yellow]D[-]elete"}).
		SetDoneFunc(func(i int, _ string) {
			if i == 1 {
				doDelete()
				return
			}
			t.returnToMain()
		})
	modal.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 'c', 'C':
			t.returnToMain()
			return nil
		case 'd', 'D':
			doDelete()
			return nil
		}
		return event
	})
	t.app.SetRoot(modal, true)
}

func (t *App) showEditTagsForm(s domain.Server) {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(fmt.Sprintf(" Edit Tags: %s ", s.Alias)).SetTitleAlign(tview.AlignCenter)
	form.AddInputField("Tags (comma):", strings.Join(s.Tags, ", "), 40, nil, nil)
	form.AddButton("Save", func() {
		tags := parseTags(form.GetFormItem(0).(*tview.InputField).GetText())
		if _, err := t.store.Mutate(s.ID, func(x *domain.Server) { x.Tags = tags }); err != nil {
			t.showStatusError(err.Error())
		}
		t.returnToMain()
		t.refreshServerList()
		t.showStatusTemp("Tags updated")
	})
	form.AddButton("Cancel", t.returnToMain)
	form.SetCancelFunc(t.returnToMain)
	t.app.SetRoot(form, true).SetFocus(form)
}

func (t *App) showPortForwardForm(s domain.Server) {
	kinds := []string{string(sshx.ForwardLocal), string(sshx.ForwardRemote), string(sshx.ForwardDynamic)}
	kind := 0
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(fmt.Sprintf(" Port Forwarding: %s ", s.Alias)).SetTitleAlign(tview.AlignCenter)

	portField := tview.NewInputField().SetLabel("Port").SetFieldWidth(8)
	hostField := tview.NewInputField().SetLabel("Host").SetText("localhost").SetFieldWidth(40)
	hostPortField := tview.NewInputField().SetLabel("Host Port").SetFieldWidth(8)
	bindField := tview.NewInputField().SetLabel("Bind Address (optional)").SetFieldWidth(40)

	dd := tview.NewDropDown().SetLabel("Type")
	dd.SetOptions(kinds, func(_ string, i int) {
		kind = i
		dynamic := kinds[i] == string(sshx.ForwardDynamic)
		hostField.SetDisabled(dynamic)
		hostPortField.SetDisabled(dynamic)
	})
	dd.SetCurrentOption(0)
	form.AddFormItem(dd).AddFormItem(portField).AddFormItem(hostField).AddFormItem(hostPortField).AddFormItem(bindField)

	form.AddButton("Start", func() {
		spec := sshx.ForwardSpec{Kind: sshx.ForwardKind(kinds[kind]), Bind: strings.TrimSpace(bindField.GetText())}
		var err error
		if spec.Port, err = parsePort(portField.GetText()); err != nil {
			t.showStatusError("Invalid port: " + err.Error())
			return
		}
		if spec.Kind != sshx.ForwardDynamic {
			spec.Host = strings.TrimSpace(hostField.GetText())
			if spec.Host == "" {
				t.showStatusError("Host is required")
				return
			}
			if spec.HostPort, err = parsePort(hostPortField.GetText()); err != nil {
				t.showStatusError("Invalid host port: " + err.Error())
				return
			}
		}
		t.returnToMain()
		t.showStatusTemp("Starting port forward…")
		go func() {
			err := t.forwarder.Start(s, spec)
			t.app.QueueUpdateDraw(func() {
				if err != nil {
					t.showStatusError("Forward failed: " + err.Error())
					return
				}
				t.refreshServerList()
				t.showStatusTemp("Forwarding started: " + spec.String())
			})
		}()
	})
	form.AddButton("Cancel", t.returnToMain)
	form.SetCancelFunc(t.returnToMain)
	t.app.SetRoot(form, true).SetFocus(form)
}

func (t *App) returnToMain() {
	t.app.SetRoot(t.root, true)
	t.app.SetFocus(t.serverList)
}

func (t *App) showStatusTemp(msg string) { t.showStatusTempColor(msg, "#A0FFA0") }

func (t *App) showStatusError(msg string) { t.showStatusTempColor(msg, "#FF6B6B") }

func (t *App) showStatusTempColor(msg, color string) {
	t.statusBar.SetText("[" + color + "]" + tview.Escape(msg) + "[-]")
	time.AfterFunc(3*time.Second, func() {
		t.app.QueueUpdateDraw(func() { t.statusBar.SetText(DefaultStatusText()) })
	})
}

func parseTags(text string) []string {
	var tags []string
	seen := map[string]bool{}
	for _, part := range strings.Split(text, ",") {
		if s := strings.TrimSpace(part); s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			tags = append(tags, s)
		}
	}
	return tags
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("must be 1-65535")
	}
	return p, nil
}

func portOf(s domain.Server) int {
	if s.Port == 0 {
		return 22
	}
	return s.Port
}
