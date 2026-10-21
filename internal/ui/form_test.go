package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// runCmd executes cmd and feeds back the messages that arrive quickly (saves,
// spinner ticks), skipping slow ones such as toast timers.
func runCmd(a *App, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		switch msg := msg.(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				runCmd(a, c)
			}
		case savedMsg, loginMsg, trustMsg, reachMsg, tailscaleMsg:
			_, next := a.Update(msg)
			runCmd(a, next)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

// saveKey presses ctrl+s and waits for the save.
func saveKey(a *App) {
	_, cmd := a.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	runCmd(a, cmd)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNewHost(t *testing.T) {
	e := newTestApp(t, 120, 30)
	a := e.a
	press(a, "n")
	if a.form == nil || !strings.Contains(screen(a), "New host") {
		t.Fatalf("n opens the form:\n%s", screen(a))
	}
	press(a, "pi5", "tab", "pi5.lan", "tab", "alice", "tab", "2222")
	saveKey(a)
	if a.form != nil {
		t.Fatalf("form still open: %q", a.form.err)
	}
	want := "Host pi5\n    HostName pi5.lan\n    User alice\n    Port 2222\n\nHost *\n"
	if got := readFile(t, e.sshPath); !strings.Contains(got, want) {
		t.Fatalf("new host goes before Host *:\n%s", got)
	}
	if cursorAlias(a) != "pi5" || !strings.Contains(a.toast.text, "Added pi5") {
		t.Fatalf("cursor %s toast %q", cursorAlias(a), a.toast.text)
	}
	if entries, _ := os.ReadDir(a.backupDir); len(entries) != 1 {
		t.Fatalf("one backup: %v", entries)
	}
}

func TestFormChecks(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	press(a, "n")
	cases := []struct{ typed, field, want string }{
		{"", "", "Give the host an alias"},
		{"elm", "", "elm already exists"},
		{"web*", "", "pattern"},
		{"box", "port", "Port is a number"},
	}
	for _, c := range cases {
		a.form.inputs[fAlias].SetValue(c.typed)
		a.form.inputs[fPort].SetValue("")
		if c.field == "port" {
			a.form.inputs[fPort].SetValue("abc")
		}
		saveKey(a)
		if a.form == nil || !strings.Contains(a.form.err, c.want) {
			t.Fatalf("%q: want error %q, form %+v", c.typed, c.want, a.form)
		}
	}
	if !strings.Contains(screen(a), "Port is a number") {
		t.Fatalf("the error is shown:\n%s", screen(a))
	}
}

func TestEditHost(t *testing.T) {
	e := newTestApp(t, 120, 30)
	a := e.a
	a.cfg.SetPinned("elm", true)
	a.refresh()
	a.hosts.focus("elm")
	press(a, "e")
	if a.form == nil || a.form.inputs[fHostName].Value() != "elm.lan" {
		t.Fatalf("e opens the form filled in: %+v", a.form)
	}
	a.form.inputs[fAlias].SetValue("elm2")
	a.form.inputs[fHostName].SetValue("elm.example")
	a.form.inputs[fUser].SetValue("")
	saveKey(a)
	if a.form != nil {
		t.Fatalf("form still open: %q", a.form.err)
	}
	want := strings.Replace(testSSHConfig, "Host elm\n    HostName elm.lan\n    User root\n", "Host elm2\n    HostName elm.example\n", 1)
	if got := readFile(t, e.sshPath); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if pinned, _ := a.cfg.HostMarks(); !pinned["elm2"] || pinned["elm"] {
		t.Fatalf("the pin follows the rename: %v", a.cfg.Pinned)
	}
	if cursorAlias(a) != "elm2" {
		t.Fatalf("cursor on %s", cursorAlias(a))
	}
}

func TestEditNoChanges(t *testing.T) {
	e := newTestApp(t, 120, 30)
	press(e.a, "e")
	saveKey(e.a)
	if e.a.form != nil || e.a.toast.text != "No changes" || readFile(t, e.sshPath) != testSSHConfig {
		t.Fatalf("form %+v toast %q", e.a.form, e.a.toast.text)
	}
}

func TestDeleteFromEditForm(t *testing.T) {
	e := newTestApp(t, 120, 30)
	a := e.a
	a.hosts.focus("devbox1")
	press(a, "e", "shift+tab") // from the first field back round to Delete
	if a.form.focus != focusDelete {
		t.Fatalf("focus %d", a.form.focus)
	}
	press(a, "enter")
	if !a.form.confirmDelete || !strings.Contains(screen(a), "Delete devbox1?") {
		t.Fatalf("delete asks first:\n%s", screen(a))
	}
	press(a, "n")
	if a.form.confirmDelete || a.form == nil {
		t.Fatal("n keeps the host")
	}
	press(a, "enter")
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(a, cmd)
	if got := readFile(t, e.sshPath); strings.Contains(got, "devbox1") || !strings.Contains(got, "Host *") {
		t.Fatalf("devbox1's block is gone:\n%s", got)
	}
	if a.form != nil || a.ssh.Find("devbox1") != nil || !strings.Contains(a.toast.text, "Deleted devbox1") {
		t.Fatalf("form %+v toast %q", a.form, a.toast.text)
	}
}

func TestDeleteSharedBlockKeepsSibling(t *testing.T) {
	e := newTestApp(t, 120, 30)
	a := e.a
	a.hosts.focus("oak2")
	press(a, "e")
	if !strings.Contains(screen(a), "Shares its block with oak") {
		t.Fatalf("shared block warning:\n%s", screen(a))
	}
	a.form.confirmDelete = true
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(a, cmd)
	if got := readFile(t, e.sshPath); !strings.Contains(got, "Host oak\n    HostName oak.example.ts.net") {
		t.Fatalf("oak keeps the block:\n%s", got)
	}
}

func TestSaveRejectedBySSH(t *testing.T) {
	e := newTestApp(t, 120, 30)
	a := e.a
	press(a, "n", "box", "tab", "BADOPT")
	saveKey(a)
	if a.form == nil || !strings.Contains(a.form.err, "line 3: Bad configuration option") || strings.Contains(a.form.err, "ssh_config") {
		t.Fatalf("ssh's complaint is shown, without the temp path: %+v", a.form)
	}
	if readFile(t, e.sshPath) != testSSHConfig {
		t.Fatal("nothing is written")
	}
}

func TestSaveRefusedWhenFileChanged(t *testing.T) {
	e := newTestApp(t, 120, 30)
	a := e.a
	press(a, "e")
	os.WriteFile(e.sshPath, []byte(testSSHConfig+"\n# edited elsewhere\n"), 0o644)
	a.form.inputs[fHostName].SetValue("other")
	saveKey(a)
	if a.form == nil || !strings.Contains(a.form.err, "changed on disk") {
		t.Fatalf("form %+v", a.form)
	}
	if !strings.HasSuffix(readFile(t, e.sshPath), "# edited elsewhere\n") {
		t.Fatal("the other change survives")
	}
}
