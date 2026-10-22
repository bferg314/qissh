package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/resolve"
	"github.com/bferg314/qissh/internal/sshconfig"
)

// The form's fields, in order.
const (
	fAlias = iota
	fHostName
	fUser
	fPort
	fIdentity
	fJump
	// The rest are qissh's own, kept in config.toml.
	fTags
	fWeb
	fNotes
	nFields
)

var fieldLabels = [nFields]string{"Alias", "HostName", "User", "Port", "IdentityFile", "ProxyJump", "Tags", "Web page", "Notes"}

// hostForm adds a host (n) or edits one (e). Focus moves through the
// fields, then the Save button, then, when editing, Delete.
type hostForm struct {
	host     *sshconfig.Host // nil for a new host
	orig     sshconfig.HostFields
	origMeta config.HostMeta
	inputs   [nFields]textinput.Model
	focus    int
	err      string
	saving   bool
	// from says where a new host was discovered ("tailnet").
	from string
	// confirmDelete asks y/n before deleting.
	confirmDelete bool
}

const (
	focusSave = nFields + iota
	focusDelete
)

type savedMsg struct {
	alias, oldAlias string
	deleted         bool
	meta            config.HostMeta
	// added lists every host when several were added at once; tagged are
	// the ones to tag "tailnet".
	added, tagged []string
	backup        string
	err           error
}

func (a *App) openForm(h *sshconfig.Host) tea.Cmd {
	if a.sshErr != nil {
		return a.notify(2, "Can't edit: %v", a.sshErr)
	}
	f := &hostForm{host: h}
	if h != nil {
		f.orig = sshconfig.FieldsOf(h)
		f.origMeta = a.cfg.Meta(h.Alias)
	}
	values := [nFields]string{f.orig.Alias, f.orig.HostName, f.orig.User, f.orig.Port, f.orig.IdentityFile, f.orig.ProxyJump,
		strings.Join(f.origMeta.Tags, " "), f.origMeta.Web, f.origMeta.Notes}
	placeholders := [nFields]string{
		"the name you'll type",
		"address or DNS name; empty uses the alias",
		"empty: " + a.defaultUser(),
		"22",
		"empty: ssh's default keys and your agent",
		"empty: connect directly",
		"home pi (filter with /#home)",
		"http://… opened with w",
		"anything worth remembering",
	}
	for i := range f.inputs {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = placeholders[i]
		in.SetValue(values[i])
		in.CursorEnd()
		f.inputs[i] = in
	}
	a.form = f
	return f.setFocus(0)
}

// defaultUser is the user ssh uses when none is set: the local one.
func (a *App) defaultUser() string {
	if name := localUser(); name != "" {
		return name
	}
	return "your local user"
}

func (f *hostForm) setFocus(i int) tea.Cmd {
	last := focusSave
	if f.host != nil {
		last = focusDelete
	}
	f.focus = (i + last + 1) % (last + 1)
	var cmd tea.Cmd
	for j := range f.inputs {
		if j == f.focus {
			cmd = f.inputs[j].Focus()
		} else {
			f.inputs[j].Blur()
		}
	}
	return cmd
}

func (f *hostForm) fields() sshconfig.HostFields {
	v := func(i int) string { return strings.TrimSpace(f.inputs[i].Value()) }
	return sshconfig.HostFields{
		Alias: v(fAlias), HostName: v(fHostName), User: v(fUser),
		Port: v(fPort), IdentityFile: v(fIdentity), ProxyJump: v(fJump),
	}
}

// meta is the host's metadata with the form's changes; what the form
// doesn't show (saved tunnels) is kept.
func (f *hostForm) meta() config.HostMeta {
	m := f.origMeta
	m.Tags = config.ParseTags(f.inputs[fTags].Value())
	m.Web = strings.TrimSpace(f.inputs[fWeb].Value())
	m.Notes = strings.TrimSpace(f.inputs[fNotes].Value())
	return m
}

func sameMeta(a, b config.HostMeta) bool {
	return slices.Equal(a.Tags, b.Tags) && a.Web == b.Web && a.Notes == b.Notes
}

func (a *App) formKey(msg tea.KeyPressMsg) tea.Cmd {
	f := a.form
	if f.saving {
		return nil
	}
	key := msg.String()
	if f.confirmDelete {
		switch key {
		case "y", "Y":
			f.confirmDelete = false
			return a.deleteHost()
		case "n", "N", "esc", "q":
			f.confirmDelete = false
		}
		return nil
	}
	switch key {
	case "esc":
		a.form = nil
		return nil
	case "tab", "down":
		return f.setFocus(f.focus + 1)
	case "shift+tab", "up":
		return f.setFocus(f.focus - 1)
	case "ctrl+s":
		return a.saveForm()
	case "enter":
		switch f.focus {
		case focusSave:
			return a.saveForm()
		case focusDelete:
			f.confirmDelete = true
			return nil
		}
		return f.setFocus(f.focus + 1)
	}
	return a.updateForm(msg)
}

// updateForm passes anything else (typing, pastes, cursor blink) to the
// focused field.
func (a *App) updateForm(msg tea.Msg) tea.Cmd {
	f := a.form
	if f.focus >= nFields {
		return nil
	}
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
	f.err = ""
	return cmd
}

// check finds what's wrong with the fields, focusing the first bad one.
func (a *App) checkForm() bool {
	f := a.form
	v := f.fields()
	bad := func(field int, msg string) bool {
		f.err = msg
		f.setFocus(field)
		return false
	}
	if e := v.Check(); e != nil {
		msg := e.Msg
		if e.Field == sshconfig.FieldAlias && sshconfig.IsPattern(v.Alias) {
			msg = strings.TrimSuffix(msg, ")") + "; use E to write one)"
		}
		// The form's first fields are HostFields', in the same order.
		return bad(int(e.Field), msg)
	}
	if strings.ContainsAny(strings.TrimSpace(f.inputs[fWeb].Value()), " \t") {
		return bad(fWeb, "A web address can't contain spaces")
	}
	if other := a.ssh.Find(v.Alias); other != nil && (f.host == nil || !strings.EqualFold(other.Alias, f.host.Alias)) {
		return bad(fAlias, fmt.Sprintf("%s already exists (%s:%d)", other.Alias, config.TildePath(other.File.Path), other.LineNo()))
	}
	return true
}

func (a *App) saveForm() tea.Cmd {
	f := a.form
	if !a.checkForm() {
		return nil
	}
	fields, meta := f.fields(), f.meta()
	if f.host != nil && fields == f.orig {
		// Only qissh's own fields changed: the ssh config stays as it is.
		a.form = nil
		if sameMeta(meta, f.origMeta) {
			return a.notify(0, "No changes")
		}
		a.cfg.SetMeta(f.host.Alias, meta)
		a.refresh()
		return tea.Batch(a.saveConfig(), a.notify(1, "Saved %s", f.host.Alias))
	}

	var file *sshconfig.File
	if f.host == nil {
		file = a.mainFile()
		file.AddHost(fields)
	} else {
		file = f.host.File.Copy()
		if err := file.SetHost(f.host.Block, f.host.Alias, fields); err != nil {
			f.err = err.Error()
			return nil
		}
	}
	old := ""
	if f.host != nil {
		old = f.host.Alias
	}
	return a.writeFile(file, savedMsg{alias: fields.Alias, oldAlias: old, meta: meta}, fields.Alias)
}

func (a *App) deleteHost() tea.Cmd {
	h := a.form.host
	file := h.File.Copy()
	if err := file.RemoveHost(h.Block, h.Alias); err != nil {
		a.form.err = err.Error()
		return nil
	}
	return a.writeFile(file, savedMsg{alias: h.Alias, deleted: true}, "qissh-check")
}

func (a *App) mainFile() *sshconfig.File { return a.ssh.MainFile() }

// writeFile saves file in the background, checking it with
// `ssh -F <file> -G <probe>` first.
func (a *App) writeFile(file *sshconfig.File, done savedMsg, probe string) tea.Cmd {
	if a.form != nil {
		a.form.saving = true
	}
	validate := resolve.Validator(a.runner, a.sshOptions(), probe)
	backups := a.backupDir
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		done.backup, done.err = sshconfig.Save(file, validate, backups)
		return done
	})
}

func (a *App) handleSaved(msg savedMsg) tea.Cmd {
	f := a.form
	if msg.err != nil {
		if f != nil {
			f.saving = false
			f.err = msg.err.Error()
			if errors.Is(msg.err, sshconfig.ErrChanged) {
				f.err = "The file changed on disk while you were editing. esc, then try again."
			}
		}
		return nil
	}
	a.form = nil
	var cmds []tea.Cmd
	if msg.oldAlias != "" && !strings.EqualFold(msg.oldAlias, msg.alias) {
		pinned, hidden := a.cfg.HostMarks()
		a.cfg.SetPinned(msg.oldAlias, false)
		a.cfg.SetHidden(msg.oldAlias, false)
		if pinned[config.HostKey(msg.oldAlias)] {
			a.cfg.SetPinned(msg.alias, true)
		}
		if hidden[config.HostKey(msg.oldAlias)] {
			a.cfg.SetHidden(msg.alias, true)
		}
		_ = a.hist.Rename(msg.oldAlias, msg.alias)
		_ = a.facts.Rename(msg.oldAlias, msg.alias)
		cmds = append(cmds, a.saveConfig())
	}
	switch {
	case msg.deleted:
		a.cfg.SetPinned(msg.alias, false)
		a.cfg.SetHidden(msg.alias, false)
		a.cfg.SetMeta(msg.alias, config.HostMeta{})
		cmds = append(cmds, a.saveConfig())
	case msg.oldAlias != "" && !strings.EqualFold(msg.oldAlias, msg.alias):
		a.cfg.SetMeta(msg.oldAlias, config.HostMeta{})
		a.cfg.SetMeta(msg.alias, msg.meta)
		cmds = append(cmds, a.saveConfig())
	case !sameMeta(a.cfg.Meta(msg.alias), msg.meta):
		a.cfg.SetMeta(msg.alias, msg.meta)
		cmds = append(cmds, a.saveConfig())
	}
	for _, alias := range msg.tagged {
		a.cfg.SetMeta(alias, config.HostMeta{Tags: []string{"tailnet"}})
	}
	if len(msg.tagged) > 0 {
		cmds = append(cmds, a.saveConfig())
	}
	a.loadSSH()
	if a.tab == tabDiscover && !msg.deleted {
		// Added from Discover: show it on Hosts, ready to connect.
		a.tab = tabHosts
		a.disc.selected = map[string]bool{}
	}
	a.hosts.focus(msg.alias)
	if len(msg.added) > 1 {
		return tea.Batch(append(cmds, a.resolveAll(), a.notify(1, "Added %s", joinAliases(msg.added)))...)
	}
	verb := "Saved"
	switch {
	case msg.deleted:
		verb = "Deleted"
	case msg.oldAlias == "":
		verb = "Added"
	}
	note := ""
	if msg.backup != "" {
		note = " · backup in " + config.TildePath(filepath.Dir(msg.backup))
	}
	return tea.Batch(append(cmds, a.resolveAll(), a.notify(1, "%s %s%s", verb, msg.alias, note))...)
}

func (a *App) renderForm() string {
	st := a.st
	f := a.form
	title := "New host"
	if f.host != nil {
		title = "Edit " + f.host.Alias
	}
	inputW := max(16, min(48, a.w-6-16))
	lines := []string{st.bold.Render(title)}
	if f.host != nil {
		lines = append(lines, st.dim.Render(fmt.Sprintf("%s:%d", config.TildePath(f.host.File.Path), f.host.LineNo())))
		if len(f.host.Siblings) > 0 {
			lines = append(lines, st.warn.Render("Shares its block with "+strings.Join(f.host.Siblings, ", ")+": option changes apply to both."))
		}
		for _, also := range f.host.Also {
			lines = append(lines, st.warn.Render(fmt.Sprintf("Also set at %s:%d, which isn't changed here.", config.TildePath(also.File.Path), also.LineNo())))
		}
	} else {
		lines = append(lines, st.dim.Render("Added to "+config.TildePath(a.ssh.Path)))
		if f.from != "" {
			title = "Add " + f.inputs[fHostName].Value()
			lines[0] = st.bold.Render(title)
			lines = append(lines, st.dim.Render("Found in "+f.from+". enter on Save adds it."))
		}
	}
	lines = append(lines, "")

	for i := range f.inputs {
		if i == fTags {
			lines = append(lines, "", "  "+st.dim.Render("qissh's own, kept in config.toml"))
		}
		in := f.inputs[i]
		in.SetWidth(inputW)
		label := st.dim.Render(fieldLabels[i])
		marker := "  "
		if i == f.focus {
			label = st.selName.Render(fieldLabels[i])
			marker = st.marker.Render("▌ ")
		}
		a.boxZone(len(lines), 0, zone{w: 16 + inputW, click: func() tea.Cmd { return f.setFocus(i) }})
		lines = append(lines, marker+fit(label, 14)+in.View())
	}
	lines = append(lines, "")
	a.boxZone(len(lines), 2, zone{w: len("[Save]"), click: func() tea.Cmd {
		f.setFocus(focusSave)
		return a.saveForm()
	}})
	if f.host != nil {
		a.boxZone(len(lines), 2+len("[Save]")+3, zone{w: len("[Delete host]"), click: func() tea.Cmd {
			f.setFocus(focusDelete)
			f.confirmDelete = true
			return nil
		}})
	}

	button := func(text string, focused bool, style lipgloss.Style) string {
		if focused {
			return style.Reverse(true).Render(" " + text + " ")
		}
		return style.Render("[" + text + "]")
	}
	buttons := "  " + button("Save", f.focus == focusSave, st.ok)
	if f.host != nil {
		buttons += "   " + button("Delete host", f.focus == focusDelete, st.bad)
	}
	lines = append(lines, buttons, "")

	switch {
	case f.saving:
		lines = append(lines, a.spin.View()+st.dim.Render(" checking with ssh and saving…"))
	case f.confirmDelete:
		what := "the whole block"
		if len(f.host.Siblings) > 0 {
			what = f.host.Alias + " from the Host line (the block stays for " + strings.Join(f.host.Siblings, ", ") + ")"
		}
		lines = append(lines, st.bad.Render("Delete "+f.host.Alias+"? ")+st.textS.Render("This removes "+what+"."),
			st.dim.Render("A backup is kept. ")+st.key.Render("y")+st.dim.Render(" delete · ")+st.key.Render("n")+st.dim.Render(" keep"))
	case f.err != "":
		lines = append(lines, st.bad.Render(f.err))
	default:
		lines = append(lines, st.dim.Render("tab/↓ next · shift+tab/↑ back · ctrl+s save · esc cancel"))
	}
	return a.boxed(st.box, lines)
}

// focus moves the cursor to alias, if it's listed.
func (t *hostsTab) focus(alias string) {
	for i, r := range t.view {
		if r.host != nil && strings.EqualFold(r.host.Alias, alias) {
			t.cursor = i
			return
		}
	}
}
