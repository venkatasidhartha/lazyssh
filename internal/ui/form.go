package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
	"lazyssh/internal/sshx"
)

var authOptions = []struct {
	pref  domain.AuthPref
	label string
}{
	{domain.AuthAuto, "Auto (password first, then SSH key)"},
	{domain.AuthPassword, "Password only (never install a key)"},
	{domain.AuthKey, "Key only (key file / ssh-agent)"},
}

var fieldHelp = map[string]string{
	"Alias":    "[::b]Alias[-]\n\nA short, unique name shown in the server list. Leave empty to use the Host/IP.\n\n[#888888]Examples:[-]\n  web-prod-1\n  db-main",
	"Host":     "[::b]Host/IP[-]\n\nIP address or DNS name of the server. [#FF6B6B](required)[-]\n\n[#888888]Examples:[-]\n  10.0.0.10\n  build.example.com",
	"User":     "[::b]User[-]\n\nRemote login user. Defaults to your local user name when empty.",
	"Port":     "[::b]Port[-]\n\nSSH port of the server. Default: 22",
	"Password": "[::b]Password[-]\n\nUsed for the first login. With Auth = Auto, lazyssh then installs its own SSH key on the server and uses the key from then on.\n\nIf the server does not allow keys (common on locked-down on-prem hosts), lazyssh keeps logging in with this password automatically.\n\nStored encrypted (AES-256-GCM) in ~/.config/lazyssh.\n\n[#55D7FF]Ctrl+R[-] shows/hides the password.",
	"Key":      "[::b]SSH Key[-]\n\nOptional path to an existing private key, e.g. ~/.ssh/id_ed25519. Press Tab for suggestions.\n\nLeave empty to let lazyssh manage a key for you.",
	"Auth":     "[::b]Authentication[-]\n\n[#55D7FF]Auto[-]: password on first login → key installed and verified → key used afterwards. Falls back to password if the server refuses keys.\n\n[#55D7FF]Password only[-]: always log in with the saved password.\n\n[#55D7FF]Key only[-]: use the key file, ssh-agent or your default ~/.ssh keys.",
	"Tags":     "[::b]Tags[-]\n\nLabels used for grouping and search. Separate several tags with commas.\n\n[#55D7FF]Type[-] to search existing tags\n[#55D7FF]↓[-] to list all existing tags\n[#55D7FF]Enter[-] to pick the highlighted tag\n[#55D7FF]New tag:[-] type it and press [#55D7FF],[-] (or pick [#55D7FF]+ new tag[-])\n[#55D7FF]Esc[-] closes the list\n\n[#888888]Example:[-]\n  prod, db, onprem",
}

type serverForm struct {
	*tview.Flex
	t        *App
	original *domain.Server
	form     *tview.Form
	help     *tview.TextView
	authIdx  int
	masked   bool

	alias, host, user, port, password, key, tags *tview.InputField
}

// showServerForm opens the Add (original == nil) or Edit server page.
func (t *App) showServerForm(original *domain.Server) {
	sf := &serverForm{
		Flex:     tview.NewFlex().SetDirection(tview.FlexRow),
		t:        t,
		original: original,
		form:     tview.NewForm(),
		help:     tview.NewTextView().SetDynamicColors(true).SetWordWrap(true),
	}
	sf.build()
	t.app.SetRoot(sf, true).SetFocus(sf.form)
}

func (sf *serverForm) build() {
	var s domain.Server
	if sf.original != nil {
		s = *sf.original
	} else {
		s = domain.Server{Port: 22, Auth: domain.AuthAuto}
	}
	port := ""
	if s.Port != 0 {
		port = strconv.Itoa(s.Port)
	}

	sf.alias = sf.input("Alias:", "Alias", s.Alias, 30, "e.g. web-prod-1")
	sf.host = sf.input("Host/IP:", "Host", s.Host, 30, "e.g. 10.0.0.10")
	sf.user = sf.input("User:", "User", s.User, 30, "e.g. root")
	sf.port = sf.input("Port:", "Port", port, 8, "22")
	sf.password = sf.input("Password:", "Password", s.Password, 30, "used for first login")
	sf.password.SetMaskCharacter('•')
	sf.masked = true
	sf.key = sf.input("SSH Key:", "Key", s.KeyPath, 40, "optional, e.g. ~/.ssh/id_ed25519")
	sf.key.SetAutocompleteFunc(keyAutocomplete)

	labels := make([]string, len(authOptions))
	for i, o := range authOptions {
		labels[i] = o.label
		if o.pref == s.Auth {
			sf.authIdx = i
		}
	}
	auth := tview.NewDropDown().SetLabel("Auth:").SetOptions(labels, func(_ string, i int) { sf.authIdx = i })
	auth.SetCurrentOption(sf.authIdx)
	auth.SetFocusFunc(func() { sf.showHelp("Auth") })
	auth.SetListStyles(tcell.StyleDefault.Background(tcell.Color235).Foreground(tcell.Color252),
		tcell.StyleDefault.Background(tcell.Color24).Foreground(tcell.Color255))
	sf.form.AddFormItem(auth)

	sf.tags = sf.input("Tags:", "Tags", joinTags(s.Tags), 40, "type to search, ↓ for all tags")
	attachTagPicker(sf.tags, tagUsage(sf.t.store.List()))

	sf.form.AddButton("Save", sf.save)
	sf.form.AddButton("Cancel", sf.t.returnToMain)
	sf.form.SetCancelFunc(sf.t.returnToMain)
	sf.form.SetFieldBackgroundColor(tcell.Color235).
		SetButtonBackgroundColor(tcell.Color238).
		SetButtonTextColor(tcell.Color252).
		SetLabelColor(tcell.Color250)

	title := " Add Server "
	if sf.original != nil {
		title = " Edit Server: " + sf.original.Alias + " "
	}
	formPanel := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(sf.form, 0, 1, true)
	formPanel.SetBorder(true).SetTitle(title).SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.Color238).SetTitleColor(tcell.Color250)

	sf.help.SetBorder(true).SetTitle(" Help ").SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.Color238).SetTitleColor(tcell.Color250)
	sf.help.SetBorderPadding(0, 0, 1, 1)
	sf.showHelp("Alias")

	main := tview.NewFlex().SetDirection(tview.FlexColumn)
	main.SetDrawFunc(func(_ tcell.Screen, x, y, w, h int) (int, int, int, int) {
		// Hide the help panel on narrow terminals, like lazyssh.
		main.Clear()
		main.AddItem(formPanel, 0, 3, true)
		if w >= 100 {
			main.AddItem(sf.help, 0, 2, false)
		}
		return x, y, w, h
	})
	main.AddItem(formPanel, 0, 3, true).AddItem(sf.help, 0, 2, false)

	hint := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	hint.SetBackgroundColor(tcell.Color235)
	hint.SetText("[white]Tab/↑↓[-] Navigate  • [white]^R[-] Show/hide password  • [white]^S[-] Save  • [white]Esc[-] Cancel")

	sf.Flex.AddItem(NewAppHeader(sf.t.version, sf.t.serverCount), 2, 0, false).
		AddItem(main, 0, 1, true).
		AddItem(hint, 1, 0, false)

	sf.Flex.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyCtrlS:
			sf.save()
			return nil
		case tcell.KeyCtrlR:
			sf.togglePasswordMask()
			return nil
		}
		return ev
	})
}

func (sf *serverForm) togglePasswordMask() {
	sf.masked = !sf.masked
	if sf.masked {
		sf.password.SetMaskCharacter('•')
		sf.masked = true
	} else {
		sf.password.SetMaskCharacter(0)
	}
}

func (sf *serverForm) input(label, helpKey, value string, width int, placeholder string) *tview.InputField {
	f := tview.NewInputField().SetLabel(label).SetText(value).SetFieldWidth(width).
		SetPlaceholder(placeholder).SetPlaceholderTextColor(tcell.Color242)
	f.SetFocusFunc(func() { sf.showHelp(helpKey) })
	sf.form.AddFormItem(f)
	return f
}

func (sf *serverForm) showHelp(key string) {
	sf.help.SetText(fieldHelp[key])
}

func (sf *serverForm) save() {
	s := domain.Server{}
	if sf.original != nil {
		s = *sf.original
	}
	s.Host = strings.TrimSpace(sf.host.GetText())
	s.Alias = strings.TrimSpace(sf.alias.GetText())
	s.User = strings.TrimSpace(sf.user.GetText())
	s.Password = sf.password.GetText()
	s.KeyPath = strings.TrimSpace(sf.key.GetText())
	s.Tags = parseTags(sf.tags.GetText())
	s.Auth = authOptions[sf.authIdx].pref

	var errs []string
	if s.Host == "" {
		errs = append(errs, "Host/IP is required")
	} else if strings.ContainsAny(s.Host, " \t@/") {
		errs = append(errs, "Host/IP must not contain spaces, '@' or '/'")
	}
	if s.Alias == "" {
		s.Alias = s.Host
	}
	if strings.ContainsAny(s.Alias, " \t") {
		errs = append(errs, "Alias must not contain spaces")
	}
	if p := strings.TrimSpace(sf.port.GetText()); p == "" {
		s.Port = 22
	} else if n, err := parsePort(p); err != nil {
		errs = append(errs, "Port must be 1-65535")
	} else {
		s.Port = n
	}
	if s.KeyPath != "" {
		if _, err := os.Stat(sshx.ExpandHome(s.KeyPath)); err != nil {
			errs = append(errs, "Key file not found: "+s.KeyPath)
		}
	}
	if s.Auth == domain.AuthPassword && s.Password == "" {
		errs = append(errs, "Password only mode needs a password")
	}
	if len(errs) > 0 {
		sf.showError(strings.Join(errs, "\n"))
		return
	}

	// Connection identity changed: learn key support again.
	if o := sf.original; o != nil && (o.Host != s.Host || o.User != s.User || portOf(*o) != s.Port ||
		o.Auth != s.Auth || o.KeyPath != s.KeyPath || (o.KeyState == domain.KeyUnsupported && o.Password != s.Password)) {
		s.KeyState, s.KeyNote = domain.KeyUnknown, ""
	}

	var err error
	if sf.original == nil {
		s, err = sf.t.store.Add(s)
	} else {
		err = sf.t.store.Update(s)
	}
	if err != nil {
		sf.showError("Save failed: " + err.Error())
		return
	}
	sf.t.returnToMain()
	// Select the saved server so Enter connects right away.
	sf.t.searchBar.SetText("")
	sf.t.refreshServerList()
	for i, x := range sf.t.serverList.servers {
		if x.ID == s.ID {
			sf.t.serverList.SetCurrentItem(i)
		}
	}
	sf.t.showStatusTemp(fmt.Sprintf("Saved %s — press Enter to connect", s.Alias))
}

func (sf *serverForm) showError(msg string) {
	modal := tview.NewModal().SetText(msg).AddButtons([]string{"OK"}).
		SetDoneFunc(func(int, string) { sf.t.app.SetRoot(sf, true).SetFocus(sf.form) })
	sf.t.app.SetRoot(modal, true)
}

// keyAutocomplete suggests private keys from ~/.ssh.
func keyAutocomplete(text string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(home, ".ssh"))
	if err != nil {
		return nil
	}
	skip := map[string]bool{"config": true, "known_hosts": true, "known_hosts.old": true, "authorized_keys": true, "environment": true}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || skip[n] || strings.HasSuffix(n, ".pub") {
			continue
		}
		p := "~/.ssh/" + n
		if text == "" || strings.Contains(strings.ToLower(p), strings.ToLower(text)) {
			out = append(out, p)
		}
	}
	if len(out) == 1 && out[0] == text {
		return nil
	}
	return out
}
