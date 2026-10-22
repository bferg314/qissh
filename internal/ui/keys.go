package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/keys"
)

type keysTab struct {
	cursor int
	loaded bool
	list   []*keys.Key
	err    error
	agent  keys.Agent
	// agentHint says how to start an agent when there isn't one.
	agentHint string
}

type keysMsg struct {
	list      []*keys.Key
	err       error
	agent     keys.Agent
	agentHint string
}

// loadKeys reads the key pairs and asks the agent what it holds.
func (a *App) loadKeys() tea.Cmd {
	dir, r, sshAdd := a.keyDir, a.runner, a.sshOptions().Tool("ssh-add")
	return func() tea.Msg {
		m := keysMsg{}
		m.list, m.err = keys.Scan(dir)
		ctx := context.Background()
		m.agent = keys.LoadAgent(ctx, r, sshAdd)
		if !m.agent.Running {
			m.agentHint = keys.AgentHint(ctx, r, sshAdd)
		}
		return m
	}
}

func (a *App) handleKeys(m keysMsg) {
	t := &a.keys
	t.loaded, t.list, t.err, t.agent, t.agentHint = true, m.list, m.err, m.agent, m.agentHint
	t.cursor = min(t.cursor, max(0, len(t.list)-1))
	a.runDoctor() // keys without a passphrase
}

func (t *keysTab) current() *keys.Key {
	if t.cursor >= 0 && t.cursor < len(t.list) {
		return t.list[t.cursor]
	}
	return nil
}

// usedBy lists the hosts that offer k, from what `ssh -G` resolved.
func (a *App) usedBy(k *keys.Key) []string {
	var out []string
	for _, h := range a.ssh.Hosts {
		if res := a.res(h.Alias); res != nil && res.Err == nil && k.UsedBy(res.IdentityFiles, config.ExpandPath) {
			out = append(out, h.Alias)
		}
	}
	return out
}

// publicKeys are the keys that can be copied to a host.
func (a *App) publicKeys() []*keys.Key {
	var out []*keys.Key
	for _, k := range a.keys.list {
		if k.Public != "" && k.Err == nil {
			out = append(out, k)
		}
	}
	return out
}

func (a *App) keysKey(key string) tea.Cmd {
	t := &a.keys
	switch key {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		t.cursor = min(len(t.list)-1, t.cursor+1)
	case "home":
		t.cursor = 0
	case "end":
		t.cursor = max(0, len(t.list)-1)
	case "r":
		return tea.Batch(a.loadKeys(), a.notify(0, "Reloaded keys"))
	case "n":
		return a.openKeyForm()
	}
	k := t.current()
	if k == nil {
		return nil
	}
	switch key {
	case "enter", "m":
		return a.keyActions(k)
	case "A":
		return a.addToAgent(k)
	case "C":
		return a.pickHostsFor(k)
	case "y":
		if k.Public == "" {
			return a.notify(2, "%s has no .pub file", k.Name)
		}
		return a.copyText(k.Name+".pub", k.Public)
	}
	return nil
}

func (a *App) keyActions(k *keys.Key) tea.Cmd {
	p := &actionPopup{title: k.Name}
	add := func(key, label, note string, run func() tea.Cmd) {
		p.actions = append(p.actions, popupAction{key: key, label: label, note: note, run: run})
	}
	if k.Public != "" {
		add("C", "Copy to hosts…", "appends it to authorized_keys", func() tea.Cmd { return a.pickHostsFor(k) })
		add("y", "Copy public key", "to the clipboard", func() tea.Cmd { return a.copyText(k.Name+".pub", k.Public) })
	}
	if k.Path != "" {
		add("A", "Add to agent", "asks for the passphrase", func() tea.Cmd { return a.addToAgent(k) })
	}
	add("n", "New key…", "ssh-keygen", a.openKeyForm)
	a.popup = p
	return nil
}

// ---- running things in the terminal ----

// terminalJob runs programs in the terminal quick-ssh hands over, one
// after another, so they can ask for passwords and passphrases. When one
// fails it waits for enter, so its complaint can be read before the
// dashboard comes back.
type terminalJob struct {
	steps []jobStep
	errs  []error

	stdin          io.Reader
	stdout, stderr io.Writer
}

type jobStep struct {
	title string // printed above the step's output when set
	argv  []string
}

func (j *terminalJob) SetStdin(r io.Reader)  { j.stdin = r }
func (j *terminalJob) SetStdout(w io.Writer) { j.stdout = w }
func (j *terminalJob) SetStderr(w io.Writer) { j.stderr = w }

func (j *terminalJob) Run() error {
	if j.stdin == nil {
		j.stdin = os.Stdin
	}
	if j.stdout == nil {
		j.stdout = os.Stdout
	}
	if j.stderr == nil {
		j.stderr = os.Stderr
	}
	j.errs = make([]error, len(j.steps))
	failed := false
	for i, s := range j.steps {
		if s.title != "" {
			fmt.Fprintf(j.stdout, "\n── %s ──\n", s.title)
		}
		cmd := exec.Command(s.argv[0], s.argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = j.stdin, j.stdout, j.stderr
		if j.errs[i] = cmd.Run(); j.errs[i] != nil {
			failed = true
		}
	}
	if failed {
		fmt.Fprint(j.stdout, "\nPress enter to go back to quick-ssh.")
		_, _ = bufio.NewReader(j.stdin).ReadString('\n')
	}
	return nil
}

// ---- generate ----

var keyTypes = []string{"ed25519", "ecdsa", "rsa"}

// keyForm creates a key with ssh-keygen (n on the Keys tab).
type keyForm struct {
	name, comment textinput.Model
	typ           int
	focus         int // 0 name, 1 type, 2 comment, 3 the button
	err           string
	// autoName is the suggested name, replaced when the type changes
	// unless it was edited.
	autoName string
}

type keyGenMsg struct {
	name string
	err  error
}

func machineName() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(strings.ToLower(h), ".")
	return h
}

func (a *App) openKeyForm() tea.Cmd {
	f := &keyForm{}
	f.name, f.comment = textinput.New(), textinput.New()
	f.name.Prompt, f.comment.Prompt = "", ""
	f.autoName = keys.FreeName(a.keyDir, keyTypes[0], machineName())
	f.name.SetValue(f.autoName)
	comment := a.defaultUser()
	if m := machineName(); m != "" {
		comment += "@" + m
	}
	f.comment.SetValue(comment)
	f.name.CursorEnd()
	f.comment.CursorEnd()
	a.keyForm = f
	return f.setFocus(0)
}

func (f *keyForm) setFocus(i int) tea.Cmd {
	f.focus = (i + 4) % 4
	f.name.Blur()
	f.comment.Blur()
	switch f.focus {
	case 0:
		return f.name.Focus()
	case 2:
		return f.comment.Focus()
	}
	return nil
}

func (a *App) keyFormKey(msg tea.KeyPressMsg) tea.Cmd {
	f := a.keyForm
	switch key := msg.String(); key {
	case "esc":
		a.keyForm = nil
		return nil
	case "tab", "down":
		return f.setFocus(f.focus + 1)
	case "shift+tab", "up":
		return f.setFocus(f.focus - 1)
	case "ctrl+s":
		return a.generateKey()
	case "enter":
		if f.focus == 3 {
			return a.generateKey()
		}
		return f.setFocus(f.focus + 1)
	case "left", "right", "space", "h", "l":
		if f.focus == 1 {
			d := 1
			if key == "left" || key == "h" {
				d = len(keyTypes) - 1
			}
			f.typ = (f.typ + d) % len(keyTypes)
			if f.name.Value() == f.autoName {
				f.autoName = keys.FreeName(a.keyDir, keyTypes[f.typ], machineName())
				f.name.SetValue(f.autoName)
				f.name.CursorEnd()
			}
			return nil
		}
	}
	return a.updateKeyForm(msg)
}

func (a *App) updateKeyForm(msg tea.Msg) tea.Cmd {
	f := a.keyForm
	var cmd tea.Cmd
	switch f.focus {
	case 0:
		f.name, cmd = f.name.Update(msg)
	case 2:
		f.comment, cmd = f.comment.Update(msg)
	}
	f.err = ""
	return cmd
}

func (a *App) generateKey() tea.Cmd {
	f := a.keyForm
	name := strings.TrimSpace(f.name.Value())
	path := filepath.Join(a.keyDir, name)
	switch {
	case name == "":
		f.err = "Give the key a file name"
	case strings.ContainsAny(name, `/\ `) || strings.HasSuffix(name, ".pub"):
		f.err = "Just a file name, without spaces, folders or .pub"
	default:
		if _, err := os.Stat(path); err == nil {
			f.err = name + " already exists"
		} else if _, err := os.Stat(path + ".pub"); err == nil {
			f.err = name + ".pub already exists"
		}
	}
	if f.err != "" {
		f.setFocus(0)
		return nil
	}
	if err := os.MkdirAll(a.keyDir, 0o700); err != nil {
		f.err = err.Error()
		return nil
	}
	a.keyForm = nil
	keygen := a.sshOptions().Tool("ssh-keygen")
	args := keys.GenerateArgs(keyTypes[f.typ], filepath.ToSlash(path), strings.TrimSpace(f.comment.Value()))
	job := &terminalJob{steps: []jobStep{{title: "ssh-keygen: a passphrase protects the key if the file is ever copied", argv: append([]string{keygen}, args...)}}}
	return tea.Exec(job, func(error) tea.Msg { return keyGenMsg{name: name, err: job.errs[0]} })
}

func (a *App) handleKeyGen(m keyGenMsg) tea.Cmd {
	if m.err != nil {
		return tea.Batch(a.loadKeys(), a.notify(2, "ssh-keygen: %v", m.err))
	}
	return tea.Sequence(a.loadKeys(), func() tea.Msg { return focusKeyMsg{m.name} },
		a.notify(1, "Created %s · A adds it to the agent · C copies it to hosts", m.name))
}

type focusKeyMsg struct{ name string }

func (t *keysTab) focus(name string) {
	for i, k := range t.list {
		if k.Name == name {
			t.cursor = i
		}
	}
}

func (a *App) renderKeyForm() string {
	st := a.st
	f := a.keyForm
	inputW := max(16, min(40, a.w-6-16))
	lines := []string{st.bold.Render("New key"), st.dim.Render("Created in " + config.TildePath(a.keyDir) + " by ssh-keygen, which asks for a passphrase"), ""}
	row := func(i int, label, value string) {
		l, marker := st.dim.Render(label), "  "
		if f.focus == i {
			l, marker = st.selName.Render(label), st.marker.Render("▌ ")
		}
		lines = append(lines, marker+fit(l, 12)+value)
	}
	f.name.SetWidth(inputW)
	f.comment.SetWidth(inputW)
	row(0, "File", f.name.View())
	var types []string
	for i, t := range keyTypes {
		if i == f.typ {
			types = append(types, st.selName.Render(t))
		} else {
			types = append(types, st.faintText.Render(t))
		}
	}
	typeHint := ""
	if f.focus == 1 {
		typeHint = st.dim.Render("  ←/→")
	}
	row(1, "Type", strings.Join(types, " · ")+typeHint)
	row(2, "Comment", f.comment.View())
	lines = append(lines, "")
	button := st.ok.Render("[Generate]")
	if f.focus == 3 {
		button = st.ok.Reverse(true).Render(" Generate ")
	}
	lines = append(lines, "  "+button, "")
	if f.err != "" {
		lines = append(lines, st.bad.Render(f.err))
	} else {
		lines = append(lines, st.dim.Render("ed25519 is the best choice unless a host only takes rsa · esc cancels"))
	}
	return a.boxed(st.box, lines)
}

// ---- agent ----

type agentAddMsg struct {
	name string
	err  error
}

func (a *App) addToAgent(k *keys.Key) tea.Cmd {
	switch {
	case k.Path == "":
		return a.notify(2, "Only %s.pub is here; the agent needs the private key", k.Name)
	case !a.keys.agent.Running:
		return a.notify(2, "%s", a.keys.agentHint)
	case a.keys.agent.Has(k):
		return a.notify(0, "%s is already in the agent", k.Name)
	}
	job := &terminalJob{steps: []jobStep{{argv: []string{a.sshOptions().Tool("ssh-add"), filepath.ToSlash(k.Path)}}}}
	return tea.Exec(job, func(error) tea.Msg { return agentAddMsg{name: k.Name, err: job.errs[0]} })
}

func (a *App) handleAgentAdd(m agentAddMsg) tea.Cmd {
	if m.err != nil {
		return tea.Batch(a.loadKeys(), a.notify(2, "ssh-add %s: %v", m.name, m.err))
	}
	return tea.Batch(a.loadKeys(), a.notify(1, "Added %s to the agent", m.name))
}

// ---- copying a key to hosts ----

// pickHostsFor starts copying k: with hosts selected on the Hosts tab it
// goes straight to the confirmation, otherwise the Hosts tab asks for
// them.
func (a *App) pickHostsFor(k *keys.Key) tea.Cmd {
	if k.Public == "" {
		return a.notify(2, "%s has no .pub file to copy", k.Name)
	}
	if sel := a.hosts.selectedAliases(); len(sel) > 0 {
		a.tab = tabHosts
		return a.confirmCopy(k, sel, "")
	}
	a.copyKey = k
	a.tab = tabHosts
	return nil
}

// copyKeyToHosts is C on the Hosts tab: the selected hosts (or the one
// under the cursor) get a key, picked from a list unless there's only
// one or one was chosen on the Keys tab.
func (a *App) copyKeyToHosts() tea.Cmd {
	aliases := a.hosts.selectedAliases()
	if len(aliases) == 0 {
		if h := a.hosts.currentHost(); h != nil {
			aliases = []string{h.Alias}
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	if k := a.copyKey; k != nil {
		return a.confirmCopy(k, aliases, "")
	}
	pubs := a.publicKeys()
	switch {
	case !a.keys.loaded:
		return a.notify(0, "Still reading your keys")
	case len(pubs) == 0:
		return a.notify(2, "No public keys in %s: make one on the Keys tab with n", config.TildePath(a.keyDir))
	case len(pubs) == 1:
		return a.confirmCopy(pubs[0], aliases, "")
	}
	p := &actionPopup{title: "Copy which key to " + joinAliases(aliases) + "?"}
	for i, k := range pubs {
		key := ""
		if i < 9 {
			key = fmt.Sprint(i + 1)
		}
		p.actions = append(p.actions, popupAction{key: key, label: k.Name, note: a.keySummary(k),
			run: func() tea.Cmd { return a.confirmCopy(k, aliases, "") }})
	}
	a.popup = p
	return nil
}

func (a *App) keySummary(k *keys.Key) string {
	s := strings.TrimSpace(fmt.Sprintf("%s %s", k.Type, k.Comment))
	if a.keys.agent.Has(k) {
		s += " · in agent"
	}
	return s
}

func joinAliases(aliases []string) string {
	if len(aliases) > 3 {
		return fmt.Sprintf("%s and %d more", strings.Join(aliases[:2], ", "), len(aliases)-2)
	}
	return strings.Join(aliases, ", ")
}

// Target systems for copying a key; "" detects.
const (
	osDetect  = ""
	osWindows = "windows"
	osUnix    = "unix"
)

// isWindows guesses whether alias is a Windows machine: Tailscale knows
// each machine's OS.
func (a *App) isWindows(alias, override string) (windows bool, why string) {
	switch override {
	case osWindows:
		return true, "chosen"
	case osUnix:
		return false, "chosen"
	}
	if h := a.ssh.Find(alias); h != nil {
		if p := a.peer(h); p != nil && p.OS != "" {
			return strings.EqualFold(p.OS, "windows"), "Tailscale"
		}
	}
	if f, ok := a.facts.Get(alias); ok && f.Err == "" {
		return f.Windows, "facts"
	}
	return false, "default"
}

// confirmCopy says where the key will go on each host before copying.
func (a *App) confirmCopy(k *keys.Key, aliases []string, override string) tea.Cmd {
	a.copyKey = nil
	p := &actionPopup{title: fmt.Sprintf("Copy %s to %s", k.Name, joinAliases(aliases))}
	w := 0
	for _, al := range aliases {
		w = max(w, len(al))
	}
	anyWindows := false
	for _, al := range aliases {
		win, why := a.isWindows(al, override)
		where := "Linux/macOS · ~/.ssh/authorized_keys"
		if win {
			anyWindows = true
			where = `Windows · administrators_authorized_keys for admins, else ~\.ssh\authorized_keys`
		}
		if why != "chosen" {
			where += " (" + why + ")"
		}
		p.body = append(p.body, fmt.Sprintf("%-*s  %s", w, al, where))
	}
	p.body = append(p.body, "", "ssh may ask for each host's password. The key is added only if", "it isn't there yet, then each login is checked.")
	p.actions = append(p.actions, popupAction{key: "enter", label: "Copy", note: "in this terminal",
		run: func() tea.Cmd { return a.copyKeyRun(k, aliases, override) }})
	next, label := osWindows, "It's Windows"
	switch {
	case override == osWindows:
		next, label = osUnix, "It's Linux/macOS"
	case override == osUnix:
		next, label = osDetect, "Detect system"
	case anyWindows:
		next, label = osUnix, "It's Linux/macOS"
	}
	p.actions = append(p.actions, popupAction{key: "w", label: label,
		run: func() tea.Cmd { return a.confirmCopy(k, aliases, next) }})
	a.popup = p
	return nil
}

type copyDoneMsg struct {
	key     string
	aliases []string
	errs    []error
}

// copyJob builds the ssh commands that install k on each alias.
func (a *App) copyJob(k *keys.Key, aliases []string, override string) *terminalJob {
	job := &terminalJob{}
	for _, al := range aliases {
		remote := keys.UnixCommand(k.Public)
		if win, _ := a.isWindows(al, override); win {
			remote = keys.WindowsCommand(k.Public)
		}
		job.steps = append(job.steps, jobStep{title: k.Name + " → " + al, argv: append(SSHArgv(a.cfg, al), remote)})
	}
	return job
}

func (a *App) copyKeyRun(k *keys.Key, aliases []string, override string) tea.Cmd {
	job := a.copyJob(k, aliases, override)
	a.hosts.clearSelection()
	return tea.Exec(job, func(error) tea.Msg { return copyDoneMsg{key: k.Name, aliases: aliases, errs: job.errs} })
}

func (a *App) handleCopyDone(m copyDoneMsg) tea.Cmd {
	var ok, failed []string
	for i, al := range m.aliases {
		if i < len(m.errs) && m.errs[i] == nil {
			ok = append(ok, al)
		} else {
			failed = append(failed, al)
		}
	}
	var note tea.Cmd
	switch {
	case len(ok) == 0:
		note = a.notify(2, "Couldn't copy %s to %s", m.key, joinAliases(failed))
	case len(failed) > 0:
		note = a.notify(2, "Copied %s to %s; failed on %s · checking logins", m.key, joinAliases(ok), joinAliases(failed))
	default:
		note = a.notify(1, "Copied %s to %s · checking %s", m.key, joinAliases(ok), plural(len(ok), "login", "logins"))
	}
	return tea.Batch(note, a.checkLoginsFor(ok))
}

// ---- view ----

func (a *App) renderKeys(h int) string {
	st := a.st
	t := &a.keys
	w := a.w
	var lines []string
	switch {
	case !t.loaded:
		return "\n  " + a.spin.View() + st.dim.Render(" reading "+config.TildePath(a.keyDir)+"…")
	case t.err != nil:
		return "\n  " + st.bad.Render("Can't read "+config.TildePath(a.keyDir)+": "+t.err.Error())
	case len(t.list) == 0:
		lines = append(lines, "", "  "+st.dim.Render("No keys in "+config.TildePath(a.keyDir)+" yet. ")+st.key.Render("n")+st.dim.Render(" makes one."))
	default:
		lines = append(lines, a.keyTable(w)...)
		if k := t.current(); k != nil {
			lines = append(lines, "")
			lines = append(lines, a.keyDetail(k, w)...)
		}
	}

	lines = append(lines, "")
	switch {
	case t.agent.Running:
		n := len(t.agent.Fingerprints)
		lines = append(lines, "  "+st.colHead.Render("AGENT")+"  "+st.ok.Render("running")+st.dim.Render(fmt.Sprintf(" · %d %s loaded", n, plural(n, "key", "keys"))))
	default:
		lines = append(lines, "  "+st.colHead.Render("AGENT")+"  "+st.warn.Render("not available"))
		for _, l := range wrap(t.agentHint, max(20, w-9)) {
			lines = append(lines, "         "+st.dim.Render(l))
		}
	}
	lines = append(lines, "", "  "+st.key.Render("n")+st.dim.Render(" new key  ")+st.key.Render("C")+st.dim.Render(" copy to hosts  ")+
		st.key.Render("A")+st.dim.Render(" add to agent  ")+st.key.Render("y")+st.dim.Render(" copy public key  ")+st.key.Render("enter")+st.dim.Render(" all actions"))
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

func (a *App) keyTable(w int) []string {
	st := a.st
	t := &a.keys
	nameW := len("KEY")
	for _, k := range t.list {
		nameW = max(nameW, len(k.Name))
	}
	nameW = min(nameW, 28)
	const typeW, agentW, passW, hostsW = 11, 9, 13, 12
	fpW := 0
	if w >= markW+nameW+typeW+agentW+passW+hostsW+12+30 {
		fpW = min(52, w-(markW+nameW+typeW+agentW+passW+hostsW+12))
	}
	head := strings.Repeat(" ", markW) + fit(st.colHead.Render("KEY"), nameW) + "  " + fit(st.colHead.Render("TYPE"), typeW) + "  " +
		fit(st.colHead.Render("AGENT"), agentW) + "  " + fit(st.colHead.Render("PASSPHRASE"), passW) + "  " + fit(st.colHead.Render("HOSTS"), hostsW)
	if fpW > 0 {
		head += "  " + st.colHead.Render("FINGERPRINT")
	}
	lines := []string{head}
	for i, k := range t.list {
		marker := "    "
		name := st.textS.Render(k.Name)
		if i == t.cursor {
			marker = st.marker.Render("▌") + "   "
			name = st.selName.Render(k.Name)
		}
		typ := st.faintText.Render("?")
		if k.Type != "" {
			typ = st.textS.Render(strings.TrimSpace(fmt.Sprintf("%s %s", k.Type, bitsText(k.Bits))))
		}
		agent := st.faintText.Render("–")
		if t.agent.Has(k) {
			agent = st.ok.Render("● loaded")
		}
		var pass string
		switch k.Protection {
		case keys.Passphrase:
			pass = st.ok.Render("✓ yes")
		case keys.NoPassphrase:
			pass = st.warn.Render("⚠ none")
		default:
			if k.Path == "" {
				pass = st.faintText.Render("no private key")
			} else {
				pass = st.faintText.Render("?")
			}
		}
		used := a.usedBy(k)
		hosts := st.faintText.Render("–")
		switch {
		case len(used) > 0 && len(used) == len(a.ssh.Hosts):
			hosts = st.textS.Render(fmt.Sprintf("all %d", len(used)))
		case len(used) > 0:
			hosts = st.textS.Render(fmt.Sprint(len(used)))
		}
		line := marker + fit(name, nameW) + "  " + fit(typ, typeW) + "  " + fit(agent, agentW) + "  " + fit(pass, passW) + "  " + fit(hosts, hostsW)
		if fpW > 0 {
			line += "  " + st.dim.Render(shorten(k.Fingerprint, fpW))
		}
		lines = append(lines, line)
	}
	return lines
}

func bitsText(bits int) string {
	if bits == 0 {
		return ""
	}
	return fmt.Sprint(bits)
}

// keyDetail describes the selected key under the table.
func (a *App) keyDetail(k *keys.Key, w int) []string {
	st := a.st
	label := func(s string) string { return "    " + st.dim.Render(fmt.Sprintf("%-12s", s)) }
	cw := max(10, w-16)
	var lines []string
	lines = append(lines, "  "+st.colHead.Render(strings.ToUpper(k.Name)))
	if k.Path != "" {
		lines = append(lines, label("private")+st.textS.Render(config.TildePath(k.Path)))
	}
	if k.PubPath != "" {
		lines = append(lines, label("public")+st.textS.Render(config.TildePath(k.PubPath)))
	} else {
		lines = append(lines, label("public")+st.warn.Render("no .pub file")+st.dim.Render(" · ssh-keygen -y -f "+config.TildePath(k.Path)+" > "+config.TildePath(k.Path)+".pub recreates it"))
	}
	if k.Err != nil {
		lines = append(lines, label("")+st.bad.Render(k.Err.Error()))
	}
	if k.Fingerprint != "" {
		lines = append(lines, label("fingerprint")+st.textS.Render(k.Fingerprint))
	}
	if k.Comment != "" {
		lines = append(lines, label("comment")+st.textS.Render(k.Comment))
	}
	if k.Protection == keys.NoPassphrase {
		lines = append(lines, label("passphrase")+st.warn.Render("none: anyone who copies the file can use it")+st.dim.Render(" · ssh-keygen -p -f "+config.TildePath(k.Path)+" adds one"))
	}
	used := a.usedBy(k)
	if len(used) == 0 {
		lines = append(lines, label("used by")+st.faintText.Render("no host names it (agent keys are still offered)"))
	} else {
		for i, l := range wrap(strings.Join(used, ", "), cw) {
			lab := ""
			if i == 0 {
				lab = "used by"
			}
			lines = append(lines, label(lab)+st.textS.Render(l))
		}
	}
	return lines
}

// wrap splits s into lines of at most w columns at spaces.
func wrap(s string, w int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
