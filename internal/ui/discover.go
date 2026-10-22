package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/discover"
	"github.com/bferg314/qissh/internal/launcher"
	"github.com/bferg314/qissh/internal/sshconfig"
)

// openURL opens a web page; tests replace it.
var openURL = launcher.OpenURL

type discoverTab struct {
	cursor, offset int
	filter         textinput.Model
	loaded         bool
	known          []discover.KnownHost
	knownErr       error
	// all is every device not in the ssh config; view is what's shown
	// after hiding and filtering.
	all, view  []*discover.Device
	selected   map[string]bool // by device key
	showHidden bool
}

type knownHostsMsg struct {
	list []discover.KnownHost
	err  error
}

func newDiscoverTab() discoverTab {
	f := textinput.New()
	f.Prompt = ""
	f.Placeholder = "name, address or OS"
	return discoverTab{filter: f, selected: map[string]bool{}}
}

// knownHostsFile is the known_hosts beside your keys.
func (a *App) knownHostsFile() string { return filepath.Join(a.keyDir, "known_hosts") }

// loadKnownHosts reads known_hosts; a missing file is no error.
func (a *App) loadKnownHosts() tea.Cmd {
	path := a.knownHostsFile()
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return knownHostsMsg{list: discover.ParseKnownHosts(data), err: err}
	}
}

func (a *App) handleKnownHosts(m knownHostsMsg) {
	t := &a.disc
	t.loaded, t.known, t.knownErr = true, m.list, m.err
	a.refreshDiscover()
}

// refreshDiscover works out which devices aren't configured, from the
// hosts as ssh resolves them (or as written, while that loads).
func (a *App) refreshDiscover() {
	t := &a.disc
	var hosts []discover.Configured
	for _, h := range a.ssh.Hosts {
		c := discover.Configured{Alias: h.Alias, HostName: h.Value("hostname")}
		if res := a.res(h.Alias); res != nil && res.Err == nil {
			c.HostName, c.Port = res.HostName, res.Port
		} else if p, err := strconv.Atoi(h.Value("port")); err == nil {
			c.Port = p
		}
		hosts = append(hosts, c)
	}
	// Stay on the same device as the list changes, unless the cursor is at
	// the top: then it stays there as Tailscale's machines arrive.
	var cur string
	if d := t.current(); d != nil && t.cursor > 0 {
		cur = d.Key()
	}
	t.all = discover.Find(a.checks.peers, t.known, hosts)

	q := strings.ToLower(strings.TrimSpace(t.filter.Value()))
	t.view = t.view[:0:0]
	keys := map[string]bool{}
	for _, d := range t.all {
		keys[d.Key()] = true
		if a.discoverHidden(d) && !t.showHidden {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(deviceText(d)), q) {
			continue
		}
		t.view = append(t.view, d)
	}
	for k := range t.selected {
		if !keys[k] {
			delete(t.selected, k) // added, or gone from the tailnet
		}
	}
	t.cursor = max(0, min(t.cursor, len(t.view)-1))
	for i, d := range t.view {
		if d.Key() == cur {
			t.cursor = i
		}
	}
}

// deviceText is what the filter matches.
func deviceText(d *discover.Device) string {
	s := d.Name + " " + d.Address + " " + strings.Join(d.Others, " ")
	if d.Peer != nil {
		s += " " + d.Peer.OS + " " + strings.Join(d.Peer.TailscaleIPs, " ")
	}
	return s
}

func (t *discoverTab) current() *discover.Device {
	if t.cursor >= 0 && t.cursor < len(t.view) {
		return t.view[t.cursor]
	}
	return nil
}

// discoverHidden reports a device left off the list: hidden with h, or
// a phone or tablet.
func (a *App) discoverHidden(d *discover.Device) bool {
	return d.Handheld() || slices.ContainsFunc(a.cfg.DiscoverHidden, func(k string) bool { return strings.EqualFold(k, d.Key()) })
}

func (a *App) discoverKey(key string) tea.Cmd {
	t := &a.disc
	page := max(1, a.h-14)
	switch key {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		t.cursor = max(0, min(len(t.view)-1, t.cursor+1))
	case "pgup":
		t.cursor = max(0, t.cursor-page)
	case "pgdown":
		t.cursor = max(0, min(len(t.view)-1, t.cursor+page))
	case "home":
		t.cursor = 0
	case "end":
		t.cursor = max(0, len(t.view)-1)
	case "r":
		return tea.Batch(a.loadKnownHosts(), a.loadTailscale(), a.notify(0, "Reloaded known_hosts and Tailscale"))
	case "H":
		t.showHidden = !t.showHidden
		a.refreshDiscover()
		if t.showHidden {
			return a.notify(0, "Showing hidden devices")
		}
		return a.notify(0, "Hiding hidden devices")
	case "a":
		all := len(t.view) > 0
		for _, d := range t.view {
			all = all && t.selected[d.Key()]
		}
		for _, d := range t.view {
			if all {
				delete(t.selected, d.Key())
			} else {
				t.selected[d.Key()] = true
			}
		}
		return nil
	}
	d := t.current()
	if d == nil {
		return nil
	}
	switch key {
	case "space":
		if t.selected[d.Key()] {
			delete(t.selected, d.Key())
		} else {
			t.selected[d.Key()] = true
		}
		t.cursor = min(len(t.view)-1, t.cursor+1)
	case "enter":
		if sel := a.selectedDevices(); len(sel) > 0 {
			return a.confirmAdd(sel)
		}
		return a.addDevice(d)
	case "m":
		return a.deviceActions(d)
	case "w":
		return a.openAdmin(d)
	case "y":
		return a.copyText(d.Address, d.Address)
	case "h":
		if d.Handheld() {
			return a.notify(0, "Phones and tablets are always hidden; H shows them")
		}
		on := !a.discoverHidden(d)
		a.cfg.SetDiscoverHidden(d.Key(), on)
		delete(t.selected, d.Key())
		a.refreshDiscover()
		if on {
			return tea.Batch(a.saveConfig(), a.notify(0, "Hid %s (H shows hidden devices)", d.Name))
		}
		return tea.Batch(a.saveConfig(), a.notify(0, "Unhid %s", d.Name))
	}
	return nil
}

func (a *App) selectedDevices() []*discover.Device {
	var out []*discover.Device
	for _, d := range a.disc.view {
		if a.disc.selected[d.Key()] {
			out = append(out, d)
		}
	}
	return out
}

func (a *App) deviceActions(d *discover.Device) tea.Cmd {
	p := &actionPopup{title: d.Name}
	add := func(key, label, note string, run func() tea.Cmd) {
		p.actions = append(p.actions, popupAction{key: key, label: label, note: note, run: run})
	}
	add("enter", "Add as a host…", "opens the host form, filled in", func() tea.Cmd { return a.addDevice(d) })
	if d.Peer != nil {
		add("w", "Open in Tailscale", "the admin console's machine page", func() tea.Cmd { return a.openAdmin(d) })
	}
	add("y", "Copy address", d.Address, func() tea.Cmd { return a.copyText(d.Address, d.Address) })
	if a.discoverHidden(d) {
		add("h", "Unhide", "", func() tea.Cmd { return a.discoverKey("h") })
	} else {
		add("h", "Hide", "leave it off this list", func() tea.Cmd { return a.discoverKey("h") })
	}
	a.popup = p
	return nil
}

// openAdmin opens the device in Tailscale's admin console.
func (a *App) openAdmin(d *discover.Device) tea.Cmd {
	if d.Peer == nil {
		return a.notify(0, "%s isn't on your tailnet", d.Name)
	}
	u := "https://login.tailscale.com/admin/machines?q=" + url.QueryEscape(d.Peer.ShortName())
	if err := openURL(u); err != nil {
		return a.notify(2, "Opening %s: %v", u, err)
	}
	return a.notify(1, "Opened %s in the Tailscale admin console", d.Name)
}

// usualUser is the User most of your hosts log in as, when it isn't the
// local user ssh would use anyway.
func (a *App) usualUser() string {
	count := map[string]int{}
	best := ""
	for _, h := range a.ssh.Hosts {
		if res := a.res(h.Alias); res != nil && res.Err == nil && res.User != "" {
			count[res.User]++
			if count[res.User] > count[best] || count[res.User] == count[best] && res.User < best {
				best = res.User
			}
		}
	}
	if strings.EqualFold(best, a.defaultUser()) {
		return ""
	}
	return best
}

// deviceFields is what a device is added as. taken reports aliases
// already in use, so several devices added together don't collide.
func (a *App) deviceFields(d *discover.Device, taken func(string) bool) sshconfig.HostFields {
	f := sshconfig.HostFields{
		Alias:    discover.FreeAlias(d.Alias(), taken),
		HostName: d.Address,
		User:     a.usualUser(),
	}
	if d.Port != 0 {
		f.Port = strconv.Itoa(d.Port)
	}
	return f
}

func (a *App) aliasTaken(s string) bool { return a.ssh.Find(s) != nil }

// addDevice opens the host form filled in for d, with Save focused when
// it has an alias, so enter adds it.
func (a *App) addDevice(d *discover.Device) tea.Cmd {
	cmd := a.openForm(nil)
	f := a.form
	if f == nil {
		return cmd
	}
	f.from = d.Source()
	v := a.deviceFields(d, a.aliasTaken)
	for i, s := range map[int]string{fAlias: v.Alias, fHostName: v.HostName, fUser: v.User, fPort: v.Port} {
		f.inputs[i].SetValue(s)
		f.inputs[i].CursorEnd()
	}
	if d.Peer != nil {
		f.inputs[fTags].SetValue("tailnet")
	}
	if v.Alias == "" {
		return cmd // an IP address: needs a name
	}
	return f.setFocus(focusSave)
}

// confirmAdd shows what several devices will be added as.
func (a *App) confirmAdd(devs []*discover.Device) tea.Cmd {
	fields := a.devicesFields(devs)
	p := &actionPopup{title: fmt.Sprintf("Add %d %s to %s", len(devs), plural(len(devs), "host", "hosts"), config.TildePath(a.ssh.Path))}
	aliasW := 0
	for _, f := range fields {
		aliasW = max(aliasW, len(f.Alias))
	}
	for _, f := range fields {
		target := f.HostName
		if f.User != "" {
			target = f.User + "@" + target
		}
		if f.Port != "" {
			target += ":" + f.Port
		}
		p.body = append(p.body, fmt.Sprintf("%-*s  %s", aliasW, f.Alias, target))
	}
	p.actions = []popupAction{
		{key: "enter", label: "Add them", note: "checked with ssh and backed up, like any edit", run: func() tea.Cmd { return a.addDevices(devs) }},
		{key: "esc", label: "Cancel", run: func() tea.Cmd { return nil }},
	}
	a.popup = p
	return nil
}

// devicesFields names several devices at once. An IP address with no
// name becomes 10-0-0-5.
func (a *App) devicesFields(devs []*discover.Device) []sshconfig.HostFields {
	used := map[string]bool{}
	taken := func(s string) bool { return a.aliasTaken(s) || used[strings.ToLower(s)] }
	var out []sshconfig.HostFields
	for _, d := range devs {
		f := a.deviceFields(d, taken)
		if f.Alias == "" {
			f.Alias = discover.FreeAlias(strings.NewReplacer(".", "-", ":", "-").Replace(d.Address), taken)
		}
		used[strings.ToLower(f.Alias)] = true
		out = append(out, f)
	}
	return out
}

// addDevices writes several hosts in one save.
func (a *App) addDevices(devs []*discover.Device) tea.Cmd {
	if a.sshErr != nil {
		return a.notify(2, "Can't edit: %v", a.sshErr)
	}
	fields := a.devicesFields(devs)
	file := a.mainFile()
	var aliases []string
	for _, f := range fields {
		file.AddHost(f)
		aliases = append(aliases, f.Alias)
	}
	done := savedMsg{alias: aliases[0], added: aliases}
	for _, d := range devs {
		if d.Peer != nil {
			done.tagged = append(done.tagged, aliases[slices.Index(devs, d)])
		}
	}
	return a.writeFile(file, done, aliases[0])
}

// ---- view ----

func (a *App) renderDiscover(h int) string {
	st := a.st
	t := &a.disc
	w := a.w
	var top []string
	if t.filter.Focused() || t.filter.Value() != "" {
		top = append(top, " "+st.key.Render("/")+" "+t.filter.View())
	}
	top = append(top, a.discoverSources()...)
	top = append(top, "")

	var detail []string
	if d := t.current(); d != nil {
		detail = append([]string{""}, a.deviceDetail(d, w)...)
	}
	footer := []string{"", "  " + st.key.Render("enter") + st.dim.Render(" add  ") + st.key.Render("space") + st.dim.Render(" select  ") +
		st.key.Render("w") + st.dim.Render(" Tailscale admin  ") + st.key.Render("y") + st.dim.Render(" copy address  ") +
		st.key.Render("h") + st.dim.Render(" hide  ") + st.key.Render("m") + st.dim.Render(" all actions")}

	if len(t.view) == 0 {
		msg := "Every machine on your tailnet and in known_hosts is in your ssh config."
		switch {
		case !t.loaded:
			msg = "Reading known_hosts…"
		case t.filter.Value() != "":
			msg = "Nothing matches that filter."
		case len(t.all) > 0:
			msg = "Everything left is hidden. H shows hidden devices."
		}
		return strings.Join(append(top, "  "+st.dim.Render(msg)), "\n")
	}

	rows := max(1, h-len(top)-1-len(detail)-len(footer))
	if rows < 3 {
		// Short screens: the list matters more than the detail.
		detail, footer = nil, nil
		rows = max(1, h-len(top)-1)
	}
	table := a.deviceTable(w, rows)
	lines := append(top, table...)
	lines = append(lines, detail...)
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}

// discoverSources says what was searched and what couldn't be.
func (a *App) discoverSources() []string {
	st := a.st
	t := &a.disc
	var ts string
	switch {
	case !a.cfg.Tailscale:
		ts = st.dim.Render("Tailscale is off in Settings")
	case !config.Available("tailscale"):
		ts = st.dim.Render("Tailscale isn't installed")
	case a.checks.tsErr != nil:
		ts = st.warn.Render("Tailscale: " + a.checks.tsErr.Error())
	default:
		ts = st.dim.Render(fmt.Sprintf("%d tailnet %s", len(a.checks.peers), plural(len(a.checks.peers), "machine", "machines")))
	}
	kh := st.dim.Render(fmt.Sprintf("%d known_hosts %s", len(t.known), plural(len(t.known), "entry", "entries")))
	if t.knownErr != nil {
		kh = st.warn.Render("known_hosts: " + t.knownErr.Error())
	}
	n := len(t.view)
	head := "  " + st.bold.Render(fmt.Sprintf("%d %s not in your ssh config", n, plural(n, "machine", "machines"))) +
		st.dim.Render(" · from ") + ts + st.dim.Render(", ") + kh
	hidden := 0
	for _, d := range t.all {
		if a.discoverHidden(d) {
			hidden++
		}
	}
	if hidden > 0 && !t.showHidden {
		head += st.dim.Render(fmt.Sprintf(" · %d hidden", hidden))
	}
	return []string{head}
}

func (a *App) deviceTable(w, rows int) []string {
	st := a.st
	t := &a.disc
	nameW := len("MACHINE")
	for _, d := range t.view {
		nameW = max(nameW, len(d.Name))
	}
	nameW = min(nameW, 24)
	const statusW, osW, srcW = 12, 9, 20
	addrW := w - markW - nameW - statusW - osW - srcW - 8
	showSrc := addrW >= 16
	if !showSrc {
		addrW += srcW + 2
	}
	if addrW < 10 {
		addrW = 0
	}
	head := strings.Repeat(" ", markW) + fit(st.colHead.Render("MACHINE"), nameW) + "  " + fit(st.colHead.Render("STATUS"), statusW) + "  " + fit(st.colHead.Render("OS"), osW)
	if showSrc {
		head += "  " + fit(st.colHead.Render("FOUND IN"), srcW)
	}
	if addrW > 0 {
		head += "  " + st.colHead.Render("ADDRESS")
	}

	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+rows {
		t.offset = t.cursor - rows + 1
	}
	t.offset = max(0, min(t.offset, len(t.view)-rows))

	lines := []string{head}
	for i := t.offset; i < min(len(t.view), t.offset+rows); i++ {
		d := t.view[i]
		marker := "  "
		name := st.textS.Render(d.Name)
		if i == t.cursor {
			marker = st.marker.Render("▌") + " "
			name = st.selName.Render(d.Name)
		}
		switch {
		case t.selected[d.Key()]:
			marker += st.ok.Render("✓") + " "
		case a.discoverHidden(d):
			marker += st.dim.Render("⊘") + " "
			if i != t.cursor {
				name = st.dim.Render(d.Name)
			}
		default:
			marker += "  "
		}
		status, osCell := st.faintText.Render("–"), st.faintText.Render("–")
		if p := d.Peer; p != nil {
			if p.Online {
				status = st.ok.Render("● online")
			} else {
				status = st.dim.Render("○ " + ago(p.LastSeen))
			}
			if p.OS != "" {
				osCell = st.textS.Render(p.OS)
			}
		}
		line := marker + fit(name, nameW) + "  " + fit(status, statusW) + "  " + fit(osCell, osW)
		if showSrc {
			line += "  " + fit(st.dim.Render(d.Source()), srcW)
		}
		if addrW > 0 {
			addr := d.Address
			if d.Port != 0 {
				addr = "[" + addr + "]:" + strconv.Itoa(d.Port)
			}
			line += "  " + st.target.Render(shorten(addr, addrW))
		}
		lines = append(lines, line)
	}
	return lines
}

// deviceDetail describes the selected device and what it would be added as.
func (a *App) deviceDetail(d *discover.Device, w int) []string {
	st := a.st
	label := func(s string) string { return "    " + st.dim.Render(fmt.Sprintf("%-12s", s)) }
	cw := max(10, w-16)
	lines := []string{"  " + st.colHead.Render(strings.ToUpper(d.Name))}
	if p := d.Peer; p != nil {
		state := st.ok.Render("online")
		if !p.Online {
			state = st.dim.Render("offline, last seen " + ago(p.LastSeen))
		}
		lines = append(lines, label("tailnet")+st.textS.Render(p.Name()+"  ")+state)
		if len(p.TailscaleIPs) > 0 {
			lines = append(lines, label("addresses")+st.textS.Render(shorten(strings.Join(p.TailscaleIPs, ", "), cw)))
		}
	}
	if len(d.KeyTypes) > 0 {
		lines = append(lines, label("known_hosts")+st.textS.Render(shorten(strings.Join(d.KeyTypes, ", "), cw)))
		if len(d.Others) > 0 {
			lines = append(lines, label("also as")+st.textS.Render(shorten(strings.Join(d.Others, ", "), cw)))
		}
	} else {
		lines = append(lines, label("known_hosts")+st.faintText.Render("never connected from here"))
	}
	v := a.deviceFields(d, a.aliasTaken)
	add := "Host " + v.Alias + " · HostName " + v.HostName
	if v.Alias == "" {
		add = "HostName " + v.HostName + " (needs an alias)"
	}
	if v.User != "" {
		add += " · User " + v.User
	}
	if v.Port != "" {
		add += " · Port " + v.Port
	}
	lines = append(lines, label("adds")+st.target.Render(add))
	return lines
}
