package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/atotto/clipboard"
	"github.com/rivo/tview"

	"lazyssh/internal/domain"
	"lazyssh/internal/sessions"
	"lazyssh/internal/vterm"
)

// Persistent sessions: with the session service (w.sd) every session runs
// in the background and survives closing lazyssh; the layout (splits,
// popups, names, sizes) is saved on every change and put back on start.
// Without the service, sessions run inside lazyssh and the saved layout is
// reopened with fresh sessions.

// newNode makes the pane for a session; it is not placed or started yet.
func (w *Workspace) newNode(label, target string, s *domain.Server) *wsNode {
	app := w.t.app
	p := vterm.NewPane(label, func() { go app.QueueUpdateDraw(func() {}) })
	p.OnCopy = func(text string) {
		copyToClipboard(text)
		w.flash(fmt.Sprintf("Copied %d characters", len([]rune(text))), "#A0FFA0")
	}
	p.OnPasteRequest = func() string {
		text, _ := clipboard.ReadAll()
		return text
	}
	p.OnExit = func() { go app.QueueUpdateDraw(w.fillList) }
	p.SetFocusFunc(func() {
		// A click on a pane (or any focus change) means "type here".
		for _, n := range w.leaves() {
			if n.pane == p && (w.focused != n || w.floatFocus != nil) {
				w.focused = n
				w.floatFocus = nil
				w.mode = modeTerminal
				w.fillList()
				w.styleBorders()
				w.updateStatus()
				w.saveLayout()
			}
		}
		for _, f := range w.floats {
			if f.node.pane == p && w.floatFocus != f {
				w.focusFloat(f)
			}
		}
	})
	p.SetTitleAlign(tview.AlignLeft)
	return &wsNode{pane: p, target: target, server: s}
}

func (n *wsNode) spec() sessions.Spec {
	spec := sessions.Spec{Label: n.pane.Label(), Cwd: launchDir()}
	if n.server != nil {
		spec.ServerID = n.server.ID
	}
	return spec
}

// launchDir is where lazyssh was started; local shells open there.
func launchDir() string {
	d, _ := os.Getwd()
	return d
}

// startSession starts n's program: in the session service when there is
// one, else inside lazyssh.
func (w *Workspace) startSession(n *wsNode) {
	p := n.pane
	fail := func(err error) {
		p.Message("\x1b[38;5;203m✗ " + err.Error() + "\x1b[0m\r\n")
		p.Exited()
	}
	spec := n.spec()
	sd, reconnect := w.sd, w.t.Sessions
	go func() {
		// The service may have stopped (crashed, or "lazyssh sessions
		// stop"): start it again, or run this session inside lazyssh.
		if sd != nil && sd.Lost() {
			sd = nil
			if reconnect != nil {
				if c, err := reconnect(); err == nil {
					sd = c
				}
			}
			w.t.app.QueueUpdate(func() {
				if w.sd == nil || w.sd.Lost() {
					w.sd = sd
				}
				if sd == nil {
					w.flash("The background session service is not available: this session closes with lazyssh", "#FFD75F")
				}
			})
		}
		if sd != nil {
			id, err := sd.Open(spec, 80, 24) // the pane sends its real size when drawn
			if err != nil {
				fail(err)
				return
			}
			w.t.app.QueueUpdate(func() {
				n.sessionID = id
				w.saveLayout()
			})
			p.Attach(sd.Attach(id, p.Output(), p.Exited))
			return
		}
		cols, rows := p.Size()
		proc, err := sessions.Start(w.t.store, *w.t.connector, spec, max(cols, 80), max(rows, 24), p.Output(), p.Exited)
		if err != nil {
			fail(err)
			return
		}
		p.Attach(proc)
	}()
}

// ---------------------------------------------------------------------------
// Layout file
// ---------------------------------------------------------------------------

type savedNode struct {
	Session  string      `json:"session,omitempty"` // id in the session service
	Label    string      `json:"label,omitempty"`
	Target   string      `json:"target,omitempty"`
	ServerID string      `json:"server_id,omitempty"`
	Dir      int         `json:"dir,omitempty"`
	Weight   int         `json:"weight,omitempty"`
	Focused  bool        `json:"focused,omitempty"`
	Kids     []savedNode `json:"kids,omitempty"`
}

type savedFloat struct {
	Node       savedNode `json:"node"`
	X, Y, W, H int
	Minimized  bool `json:"minimized,omitempty"`
	Maximized  bool `json:"maximized,omitempty"`
	Active     bool `json:"active,omitempty"`
}

type savedLayout struct {
	Version  int          `json:"version"`
	Root     *savedNode   `json:"root,omitempty"`
	Floats   []savedFloat `json:"floats,omitempty"`
	Tabs     bool         `json:"tabs,omitempty"`
	NoList   bool         `json:"no_list,omitempty"`
	Zoomed   string       `json:"zoomed,omitempty"` // label path of the zoomed session
	ZoomedAt int          `json:"zoomed_at,omitempty"`
}

func (w *Workspace) layoutPath() string {
	return filepath.Join(filepath.Dir(w.t.store.Path()), "workspace.json")
}

func (w *Workspace) save(n *wsNode) savedNode {
	if n.pane != nil {
		sn := savedNode{Session: n.sessionID, Label: n.pane.Label(), Target: n.target, Weight: n.weight,
			Focused: n == w.focused}
		if n.server != nil {
			sn.ServerID = n.server.ID
		}
		return sn
	}
	sn := savedNode{Dir: n.dir, Weight: n.weight}
	for _, k := range n.kids {
		sn.Kids = append(sn.Kids, w.save(k))
	}
	return sn
}

func (w *Workspace) snapshot() savedLayout {
	l := savedLayout{Version: 1, Tabs: w.tabs, NoList: !w.sidebar}
	if w.root != nil {
		r := w.save(w.root)
		l.Root = &r
	}
	active := w.activeFloat()
	for _, f := range w.floats {
		l.Floats = append(l.Floats, savedFloat{Node: w.save(f.node), X: f.x, Y: f.y, W: f.w, H: f.h,
			Minimized: f.minimized, Maximized: f.maximized, Active: f == active})
	}
	if w.zoomed != nil {
		l.Zoomed = w.zoomed.pane.Label()
	}
	return l
}

// saveLayout writes the layout soon (changes come in bursts).
func (w *Workspace) saveLayout() {
	if w.restoring {
		return
	}
	if w.saveTimer != nil {
		w.saveTimer.Stop()
	}
	w.saveTimer = time.AfterFunc(300*time.Millisecond, func() {
		w.t.app.QueueUpdate(w.saveLayoutNow)
	})
}

func (w *Workspace) saveLayoutNow() {
	data, err := json.MarshalIndent(w.snapshot(), "", "  ")
	if err != nil {
		return
	}
	if w.sd != nil {
		w.sd.SaveLayout(data)
	}
	path := w.layoutPath()
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// restoreLayout puts back the last layout. Sessions still running in the
// service are reattached; the others are started again. It returns the
// number of sessions shown.
func (w *Workspace) restoreLayout() int {
	var l savedLayout
	var data []byte
	live := map[string]sessions.Info{}
	if w.sd != nil {
		data, _ = w.sd.Layout()
		if list, err := w.sd.List(); err == nil {
			for _, s := range list {
				live[s.ID] = s
			}
		}
	}
	if len(data) == 0 {
		data, _ = os.ReadFile(w.layoutPath())
	}
	if len(data) > 0 && json.Unmarshal(data, &l) != nil {
		l = savedLayout{}
	}

	w.restoring = true
	defer func() { w.restoring = false }()
	var focused *wsNode
	build := func(sn savedNode) *wsNode { return w.buildSaved(sn, live, &focused, false) }
	if l.Root != nil {
		w.root = build(*l.Root)
	}
	var active *floatWin
	for _, sf := range l.Floats {
		n := build(sf.Node)
		if n == nil || n.pane == nil {
			continue
		}
		f := &floatWin{node: n, x: sf.X, y: sf.Y, w: max(sf.W, minFloatW), h: max(sf.H, minFloatH),
			minimized: sf.Minimized, maximized: sf.Maximized}
		w.floats = append(w.floats, f)
		if sf.Active {
			active = f
		}
	}
	// Sessions running in the service that the layout does not know (for
	// example opened in another window): show them too.
	for id, info := range live {
		var srv *domain.Server
		if info.Spec.ServerID != "" {
			if s, ok := w.t.store.Get(info.Spec.ServerID); ok {
				srv = &s
			}
		}
		target := "this machine"
		if srv != nil {
			target = srv.Target()
		}
		n := w.newNode(info.Spec.Label, target, srv)
		n.weight = 1000
		w.attachSession(n, id)
		w.insert(n, tview.FlexColumn)
	}

	leaves := w.leaves()
	switch {
	case focused != nil:
		w.focused = focused
	case len(leaves) > 0:
		w.focused = leaves[0]
	}
	if active != nil && !active.minimized {
		w.floatFocus = active
	}
	w.tabs, w.sidebar = l.Tabs, !l.NoList
	if l.Zoomed != "" {
		for _, n := range append(leaves, floatNodes(w.floats)...) {
			if n.pane.Label() == l.Zoomed {
				w.zoomed = n
			}
		}
	}
	return len(leaves) + len(w.floats)
}

func floatNodes(fs []*floatWin) []*wsNode {
	out := make([]*wsNode, len(fs))
	for i, f := range fs {
		out[i] = f.node
	}
	return out
}

// buildSaved makes the sessions of a saved layout node: reattached when
// they are still running in the service (live), started otherwise. With
// fresh, names are made unique next to the sessions already open.
func (w *Workspace) buildSaved(sn savedNode, live map[string]sessions.Info, focused **wsNode, fresh bool) *wsNode {
	if len(sn.Kids) == 0 {
		var srv *domain.Server
		if sn.ServerID != "" {
			s, ok := w.t.store.Get(sn.ServerID)
			if !ok {
				return nil // server deleted since
			}
			srv = &s
		}
		label := sn.Label
		if fresh {
			label = w.uniqueLabel(label)
		}
		n := w.newNode(label, sn.Target, srv)
		n.weight = max(1, sn.Weight)
		if _, ok := live[sn.Session]; ok && sn.Session != "" {
			w.attachSession(n, sn.Session)
			delete(live, sn.Session)
		} else {
			w.startSession(n)
		}
		if sn.Focused || *focused == nil {
			*focused = n
		}
		return n
	}
	split := &wsNode{dir: sn.Dir, weight: max(1, sn.Weight)}
	for _, k := range sn.Kids {
		if kn := w.buildSaved(k, live, focused, fresh); kn != nil {
			kn.parent = split
			split.kids = append(split.kids, kn)
		}
	}
	switch len(split.kids) {
	case 0:
		return nil
	case 1:
		only := split.kids[0]
		only.parent, only.weight = nil, split.weight
		return only
	}
	return split
}

// attachSession shows a session that is running in the service.
func (w *Workspace) attachSession(n *wsNode, id string) {
	n.sessionID = id
	n.pane.Attach(w.sd.Attach(id, n.pane.Output(), n.pane.Exited))
}
