package ui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/history"
	"github.com/bferg314/qissh/internal/launcher"
	"github.com/bferg314/qissh/internal/sshconfig"
	"github.com/bferg314/qissh/internal/status"
)

type sortMode int

const (
	sortFrecency sortMode = iota
	sortName
	sortFile
	sortStatus
	nSorts
)

func (s sortMode) String() string { return [...]string{"most used", "name", "file order", "status"}[s] }

// row is a host, or a rule (Host * and the like, Match) listed below the
// hosts.
type row struct {
	host  *sshconfig.Host
	rule  *sshconfig.Rule
	order int // position in the config, for file order
	// pinned and hidden mirror the config; refresh keeps them current.
	pinned, hidden bool
	matches        []int
}

// key identifies the row across reloads.
func (r *row) key() string {
	if r.host != nil {
		return config.HostKey(r.host.Alias)
	}
	return fmt.Sprintf("rule:%s:%d", r.rule.File.Path, r.rule.LineNo())
}

type hostsTab struct {
	all        []*row
	view       []*row
	cursor     int
	offset     int
	sort       sortMode
	filter     textinput.Model
	showHidden bool
	selected   map[string]bool // host keys picked with space
}

func newHostsTab() hostsTab {
	f := textinput.New()
	f.Prompt = ""
	f.Placeholder = "type to filter…"
	return hostsTab{filter: f, selected: map[string]bool{}}
}

// rebuild makes rows for every host and rule in c, keeping the selection
// for hosts that still exist.
func (t *hostsTab) rebuild(c *sshconfig.Config) {
	t.all = t.all[:0]
	exists := map[string]bool{}
	for i, h := range c.Hosts {
		t.all = append(t.all, &row{host: h, order: i})
		exists[config.HostKey(h.Alias)] = true
	}
	for i, r := range c.Rules {
		t.all = append(t.all, &row{rule: r, order: len(c.Hosts) + i})
	}
	for k := range t.selected {
		if !exists[k] {
			delete(t.selected, k)
		}
	}
}

func (t *hostsTab) current() *row {
	if t.cursor >= 0 && t.cursor < len(t.view) {
		return t.view[t.cursor]
	}
	return nil
}

// currentHost is the host under the cursor, or nil on a rule.
func (t *hostsTab) currentHost() *sshconfig.Host {
	if r := t.current(); r != nil {
		return r.host
	}
	return nil
}

// hostRows are the visible host rows.
func (t *hostsTab) hostRows() []*row {
	var rs []*row
	for _, r := range t.view {
		if r.host != nil {
			rs = append(rs, r)
		}
	}
	return rs
}

// refresh rebuilds the visible list, keeping the cursor on the same row.
// stateFn gives a host's status, for sorting by it.
type stateFn func(*sshconfig.Host) status.State

func (t *hostsTab) refresh(cfg *config.Config, hist *history.History, state stateFn) {
	var keep string
	if r := t.current(); r != nil {
		keep = r.key()
	}

	pinned, hidden := cfg.HostMarks()
	var hosts, rules []*row
	for _, r := range t.all {
		r.matches = nil
		if r.rule != nil {
			rules = append(rules, r)
			continue
		}
		k := config.HostKey(r.host.Alias)
		r.pinned, r.hidden = pinned[k], hidden[k]
		if !r.hidden || t.showHidden {
			hosts = append(hosts, r)
		}
	}
	now := time.Now()
	sort.SliceStable(hosts, func(i, j int) bool { return t.less(hosts[i], hosts[j], hist, state, now) })

	if q := t.filter.Value(); q != "" {
		hosts = filterHosts(hosts, q, func(alias string) []string { return cfg.Meta(alias).Tags })
		rules = nil
	}
	t.view = append(hosts, rules...)

	t.cursor = min(t.cursor, max(0, len(t.view)-1))
	for i, r := range t.view {
		if r.key() == keep {
			t.cursor = i
			break
		}
	}
}

// filterHosts keeps hosts whose alias fuzzy-matches q, best first, then
// hosts whose HostName or a tag contains q. "#tag" matches tags only.
func filterHosts(hosts []*row, q string, tags func(alias string) []string) []*row {
	if tag, ok := strings.CutPrefix(strings.ToLower(q), "#"); ok {
		var out []*row
		for _, r := range hosts {
			if slices.ContainsFunc(tags(r.host.Alias), func(t string) bool { return strings.Contains(t, tag) }) {
				out = append(out, r)
			}
		}
		return out
	}
	names := make([]string, len(hosts))
	for i, r := range hosts {
		names[i] = r.host.Alias
	}
	var out []*row
	taken := map[*row]bool{}
	for _, m := range fuzzy.Find(q, names) {
		r := hosts[m.Index]
		r.matches = m.MatchedIndexes
		out = append(out, r)
		taken[r] = true
	}
	lq := strings.ToLower(q)
	for _, r := range hosts {
		if taken[r] {
			continue
		}
		if strings.Contains(strings.ToLower(r.host.Value("hostname")), lq) ||
			slices.ContainsFunc(tags(r.host.Alias), func(t string) bool { return strings.Contains(t, lq) }) {
			out = append(out, r)
		}
	}
	return out
}

// less puts pinned hosts first, then orders by the chosen sort.
func (t *hostsTab) less(a, b *row, hist *history.History, state stateFn, now time.Time) bool {
	if a.pinned != b.pinned {
		return a.pinned
	}
	switch t.sort {
	case sortFrecency:
		if sa, sb := hist.Score(a.host.Alias, now), hist.Score(b.host.Alias, now); sa != sb {
			return sa > sb
		}
	case sortFile:
		return a.order < b.order
	case sortStatus:
		sa, sb := state(a.host), state(b.host)
		if sa.Kind != sb.Kind {
			return sa.Kind < sb.Kind
		}
		if sa.Latency != sb.Latency {
			return sa.Latency < sb.Latency
		}
	}
	return strings.ToLower(a.host.Alias) < strings.ToLower(b.host.Alias)
}

func (t *hostsTab) hiddenCount() int {
	n := 0
	for _, r := range t.all {
		if r.hidden {
			n++
		}
	}
	return n
}

func (t *hostsTab) move(delta int) {
	t.cursor = max(0, min(len(t.view)-1, t.cursor+delta))
}

// toggle selects or unselects a host row.
func (t *hostsTab) toggle(r *row) {
	if t.selected[r.key()] {
		delete(t.selected, r.key())
	} else {
		t.selected[r.key()] = true
	}
}

// clearSelection drops the space-selection; it reports whether there was one.
func (t *hostsTab) clearSelection() bool {
	if len(t.selected) == 0 {
		return false
	}
	t.selected = map[string]bool{}
	return true
}

// selectedAliases are the selected hosts in list order.
func (t *hostsTab) selectedAliases() []string {
	var out []string
	for _, r := range t.view {
		if r.host != nil && t.selected[r.key()] {
			out = append(out, r.host.Alias)
		}
	}
	return out
}

// ---- keys ----

func (a *App) hostsKey(key string) tea.Cmd {
	if a.detailScrollKey(key) {
		return nil
	}
	t := &a.hosts
	page := max(1, a.h-8)
	switch key {
	case "up", "k":
		t.move(-1)
	case "down", "j":
		t.move(1)
	case "pgup":
		t.move(-page)
	case "pgdown":
		t.move(page)
	case "home":
		t.cursor = 0
	case "end":
		t.cursor = max(0, len(t.view)-1)
	case "enter":
		return a.connectSelected()
	case "space":
		if r := t.current(); r != nil && r.host != nil {
			t.toggle(r)
			t.move(1)
		}
	case "a":
		rows := t.hostRows()
		if len(t.selected) == len(rows) {
			t.clearSelection()
		} else {
			for _, r := range rows {
				t.selected[r.key()] = true
			}
		}
	case "m":
		return a.openActions()
	case "s":
		t.sort = (t.sort + 1) % nSorts
		a.refresh()
		return a.notify(0, "Sorted by %s", t.sort)
	case "r":
		a.loadSSH()
		return tea.Batch(a.resolveAll(), a.loadTailscale(), a.notify(0, "Reloaded %s and checking hosts", config.TildePath(a.ssh.Path)))
	case "d":
		a.toggleDetails()
	case "y":
		return a.copyCurrent()
	case "t":
		return a.checkLogins()
	case "n":
		return a.openForm(nil)
	case "e":
		if r := t.current(); r != nil && r.rule != nil {
			return a.edit(r.rule.Location)
		}
		if h := t.currentHost(); h != nil {
			return a.openForm(h)
		}
	case "E":
		return a.editCurrent()
	case "*":
		return a.togglePinned()
	case "h":
		return a.toggleHidden()
	case "H":
		return a.toggleShowHidden()
	case "C":
		return a.copyKeyToHosts()
	case "w":
		return a.openWeb()
	case "x":
		return a.openRun()
	case "i":
		return a.fetchFacts()
	case "T":
		return a.tunnelMenu()
	default:
		if h := t.currentHost(); h != nil && !reservedKeys[key] {
			for _, tool := range a.enabledTools() {
				if tool.Key == key {
					return a.launch(tool, h.Alias)
				}
			}
		}
	}
	return nil
}

// connectSelected connects to the selected hosts, or the one under the
// cursor. On a rule it opens the rule in the editor instead.
func (a *App) connectSelected() tea.Cmd {
	if sel := a.hosts.selectedAliases(); len(sel) > 0 {
		return a.connect(sel...)
	}
	r := a.hosts.current()
	switch {
	case r == nil:
		return nil
	case r.rule != nil:
		return a.edit(r.rule.Location)
	}
	return a.connect(r.host.Alias)
}

// openWeb opens the host's web page from its metadata.
func (a *App) openWeb() tea.Cmd {
	h := a.hosts.currentHost()
	if h == nil {
		return nil
	}
	url := a.cfg.Meta(h.Alias).Web
	if url == "" {
		return a.notify(0, "%s has no web page: e adds one", h.Alias)
	}
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	if err := launcher.OpenURL(url); err != nil {
		return a.notify(2, "Opening %s: %v", url, err)
	}
	return a.notify(1, "Opened %s", url)
}

func (a *App) togglePinned() tea.Cmd {
	h := a.hosts.currentHost()
	if h == nil {
		return nil
	}
	pinned, _ := a.cfg.HostMarks()
	on := !pinned[config.HostKey(h.Alias)]
	a.cfg.SetPinned(h.Alias, on)
	a.refresh()
	msg := "Pinned %s to the top"
	if !on {
		msg = "Unpinned %s"
	}
	return tea.Batch(a.saveConfig(), a.notify(1, msg, h.Alias))
}

func (a *App) toggleHidden() tea.Cmd {
	h := a.hosts.currentHost()
	if h == nil {
		return nil
	}
	_, hidden := a.cfg.HostMarks()
	on := !hidden[config.HostKey(h.Alias)]
	a.cfg.SetHidden(h.Alias, on)
	a.refresh()
	var note tea.Cmd
	switch {
	case !on:
		note = a.notify(1, "%s is no longer hidden", h.Alias)
	case a.hosts.showHidden:
		note = a.notify(1, "Hid %s", h.Alias)
	default:
		note = a.notify(1, "Hid %s · H shows hidden hosts", h.Alias)
	}
	return tea.Batch(a.saveConfig(), note)
}

func (a *App) toggleShowHidden() tea.Cmd {
	t := &a.hosts
	n := t.hiddenCount()
	if n == 0 && !t.showHidden {
		return a.notify(0, "No hidden hosts: h hides the selected one")
	}
	t.showHidden = !t.showHidden
	a.refresh()
	if t.showHidden {
		return a.notify(0, "Showing %d hidden %s · h unhides · H hides them again", n, plural(n, "host", "hosts"))
	}
	return a.notify(0, "Hiding %d %s", n, plural(n, "host", "hosts"))
}

// ---- view ----

func (a *App) renderHosts(w, h int) string {
	t := &a.hosts
	st := a.st
	var lines []string
	if len(t.view) > 0 {
		a.addZone(zone{x: 0, y: 0, w: w, h: h, wheel: func(d int) tea.Cmd { t.move(d); return nil }})
	}

	if k := a.copyKey; k != nil {
		lines = append(lines, " "+st.warn.Render("Copy "+k.Name+" to which hosts?")+st.dim.Render(" space selects · ")+
			st.key.Render("C")+st.dim.Render(" copies (to the selected hosts, or this one) · ")+st.key.Render("esc")+st.dim.Render(" cancels"))
		h--
	}
	if t.filter.Focused() || t.filter.Value() != "" {
		lines = append(lines, " "+st.key.Render("/")+" "+t.filter.View())
		h--
	}
	if a.sshErr != nil {
		lines = append(lines, "", "   "+st.bad.Render("Can't read "+config.TildePath(a.ssh.Path)+": "+a.sshErr.Error()))
		return strings.Join(lines, "\n")
	}

	if len(t.view) == 0 {
		msg := "No hosts in " + config.TildePath(a.ssh.Path) + " yet."
		switch {
		case t.filter.Value() != "":
			msg = "No host matches that filter."
		case len(a.ssh.Hosts) > 0:
			msg = "Every host is hidden. Press H to show them."
		}
		lines = append(lines, "", "   "+st.dim.Render(msg))
		return strings.Join(lines, "\n")
	}

	// Columns: host and status always; on narrow screens CONNECTED goes
	// first, then TARGET.
	var c cols
	c.alias = len("HOST")
	for _, r := range t.view {
		if r.host != nil {
			c.alias = max(c.alias, utf8.RuneCountInString(r.host.Alias))
		}
	}
	c.alias = min(c.alias, 28)
	c.status, c.last = 13, 9
	if w < 70 {
		c.last = 0
	}
	if w >= 72 {
		for _, r := range t.view {
			if r.host != nil {
				c.tags = max(c.tags, len(tagsText(a.cfg.Meta(r.host.Alias).Tags)))
			}
		}
		if c.tags > 0 {
			c.tags = max(len("TAGS"), min(c.tags, 20))
		}
	}
	c.target = w - markW - c.alias - c.status - 4
	if c.last > 0 {
		c.target -= c.last + 2
	}
	if c.tags > 0 {
		c.target -= c.tags + 2
	}
	if c.target < 10 {
		c.target = 0
	}

	head := strings.Repeat(" ", markW) + fit(st.colHead.Render("HOST"), c.alias) + "  " + fit(st.colHead.Render("STATUS"), c.status)
	if c.target > 0 {
		head += "  " + fit(st.colHead.Render("TARGET"), c.target)
	}
	if c.tags > 0 {
		head += "  " + fit(st.colHead.Render("TAGS"), c.tags)
	}
	if c.last > 0 {
		head += "  " + fit(st.colHead.Render("CONNECTED"), c.last)
	}
	a.sortZones(len(lines), c)
	lines = append(lines, head)
	h--

	// Lay out every row, then scroll so the cursor stays in view.
	var body []string
	var rowOf []int // the view index of each body line; -1 for headings
	cursorLine := 0
	for i, r := range t.view {
		if r.rule != nil && (i == 0 || t.view[i-1].rule == nil) {
			if i > 0 {
				body, rowOf = append(body, ""), append(rowOf, -1)
			}
			body = append(body, "    "+st.colHead.Render("DEFAULTS & RULES")+st.faintText.Render("  apply to the hosts above · enter edits"))
			rowOf = append(rowOf, -1)
		}
		if i == t.cursor {
			cursorLine = len(body)
		}
		body, rowOf = append(body, a.hostLine(r, i == t.cursor, c, w)), append(rowOf, i)
	}
	if cursorLine < t.offset {
		t.offset = cursorLine
	}
	if cursorLine >= t.offset+h {
		t.offset = cursorLine - h + 1
	}
	// Show the rules' heading when scrolling up to the first rule.
	if t.offset > 0 && t.offset == cursorLine && strings.Contains(body[t.offset-1], "DEFAULTS & RULES") {
		t.offset--
	}
	t.offset = max(0, min(t.offset, len(body)-h))
	end := min(len(body), t.offset+h)
	for j := t.offset; j < end; j++ {
		if rowOf[j] >= 0 {
			a.hostRowZones(rowOf[j], len(lines)+j-t.offset, w)
		}
	}
	lines = append(lines, body[t.offset:end]...)
	return strings.Join(lines, "\n")
}

// hostRowZones makes row i of the view, drawn at line y, clickable; a
// click on a host's mark column selects it as space does, without moving
// down.
func (a *App) hostRowZones(i, y, w int) {
	t := &a.hosts
	pick := func() {
		if t.filter.Focused() {
			t.filter.Blur()
			a.refresh()
		}
		if i < len(t.view) {
			t.cursor = i
		}
	}
	var mark func()
	if r := t.view[i]; r.host != nil {
		mark = func() { t.toggle(r) }
	}
	a.listRow(y, w, pick, a.hostsKey, mark)
}

// sortZones make the column headings at line y sort the list by them.
func (a *App) sortZones(y int, c cols) {
	t := &a.hosts
	by := func(s sortMode) func() tea.Cmd {
		return func() tea.Cmd {
			t.sort = s
			a.refresh()
			return a.notify(0, "Sorted by %s", s)
		}
	}
	x := markW
	a.addZone(zone{x: x, y: y, w: c.alias, h: 1, click: by(sortName)})
	x += c.alias + 2
	a.addZone(zone{x: x, y: y, w: c.status, h: 1, click: by(sortStatus)})
	x += c.status + 2
	for _, w := range []int{c.target, c.tags} {
		if w > 0 {
			x += w + 2
		}
	}
	if c.last > 0 {
		a.addZone(zone{x: x, y: y, w: c.last, h: 1, click: by(sortFrecency)})
	}
}

// markW is the cursor marker and the pin/hidden/selected mark.
const markW = 4

// cols are the Hosts list's column widths; 0 means left out.
type cols struct{ alias, status, target, tags, last int }

// tagsText is how tags are shown: "#home #pi".
func tagsText(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return "#" + strings.Join(tags, " #")
}

func (a *App) hostLine(r *row, sel bool, c cols, w int) string {
	st := a.st
	marker := "  "
	if sel {
		marker = st.marker.Render("▌") + " "
	}
	switch {
	case r.host != nil && a.hosts.selected[r.key()]:
		marker += st.ok.Render("✓") + " "
	case r.pinned:
		marker += st.pin.Render("★") + " "
	case r.hidden:
		marker += st.dim.Render("⊘") + " "
	default:
		marker += "  "
	}

	if r.rule != nil {
		text := st.dim.Render(r.rule.Text())
		if sel {
			text = st.selName.Render(r.rule.Text())
		}
		where := st.faintText.Render(fmt.Sprintf("%s:%d", config.TildePath(r.rule.File.Path), r.rule.LineNo()))
		return marker + fit(text, max(10, w-4-22)) + "  " + where
	}

	name := st.highlight(r.host.Alias, c.alias, r.matches, sel)
	if r.hidden && !sel {
		name = st.dim.Render(ansi.Strip(name))
	}
	line := marker + name + "  " + fit(a.statusCell(r.host), c.status)
	if c.target > 0 {
		line += "  " + fit(a.targetCell(r.host), c.target)
	}
	if c.tags > 0 {
		line += "  " + fit(st.tag.Render(tagsText(a.cfg.Meta(r.host.Alias).Tags)), c.tags)
	}
	if c.last > 0 {
		var last string
		if e := a.hist.Get(r.host.Alias); e != nil {
			last = st.agoStyle(e.Last).Render(ago(e.Last))
		} else {
			last = st.faintText.Render("never")
		}
		line += "  " + fit(last, c.last)
	}
	return line
}

// targetCell is where a host connects to: ssh's resolved user@host:port,
// or what the block itself says while that loads.
func (a *App) targetCell(h *sshconfig.Host) string {
	st := a.st
	res := a.res(h.Alias)
	switch {
	case res == nil:
		raw := h.Value("hostname")
		if raw == "" {
			raw = h.Alias
		}
		if u := h.Value("user"); u != "" {
			raw = u + "@" + raw
		}
		return st.faintText.Render(raw)
	case res.Err != nil:
		return st.bad.Render("ssh -G: " + res.Err.Error())
	}
	return st.target.Render(res.Target())
}
