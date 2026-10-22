package ui

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/qissh/internal/keys"
	"github.com/bferg314/qissh/internal/probe"
	"github.com/bferg314/qissh/internal/tailscale"
)

func sshString(b []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...)
}

// writeTestKeys puts two key pairs in dir: id_ed25519 without a
// passphrase and work with one. The private files hold only the header
// qissh reads. It returns id_ed25519's fingerprint.
func writeTestKeys(t *testing.T, dir string) string {
	t.Helper()
	os.MkdirAll(dir, 0o700)
	var fp string
	for _, k := range []struct{ name, cipher string }{{"id_ed25519", "none"}, {"work", "aes256-ctr"}} {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		blob := append(sshString([]byte("ssh-ed25519")), sshString(pub)...)
		line := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " me@here"
		priv := pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: append([]byte("openssh-key-v1\x00"), sshString([]byte(k.cipher))...)})
		os.WriteFile(filepath.Join(dir, k.name), priv, 0o600)
		os.WriteFile(filepath.Join(dir, k.name+".pub"), []byte(line+"\n"), 0o644)
		if k.name == "id_ed25519" {
			_, _, fp, _, _ = keys.ParsePublic(line)
		}
	}
	return fp
}

func TestKeysTab(t *testing.T) {
	a := newTestApp(t, 130, 30).a
	press(a, "3")
	s := screen(a)
	for _, want := range []string{"Keys 2", "id_ed25519", "work", "ED25519 256", "● loaded", "⚠ none", "✓ yes", "running · 1 key loaded", "SHA256:"} {
		if !strings.Contains(s, want) {
			t.Errorf("keys tab lacks %q:\n%s", want, s)
		}
	}
	// elm names id_ed25519 itself.
	if !strings.Contains(s, "used by     elm") {
		t.Errorf("which hosts use it:\n%s", s)
	}
	press(a, "j")
	if !strings.Contains(screen(a), "used by     no host names it") {
		t.Errorf("work isn't used:\n%s", screen(a))
	}
}

func TestNoAgent(t *testing.T) {
	e := newTestApp(t, 130, 30)
	e.fake.agent = ""
	e.a.runner = e.fake
	e.a.Update(e.a.loadKeys()())
	press(e.a, "3")
	if s := screen(e.a); !strings.Contains(s, "not available") || strings.Contains(s, "● loaded") {
		t.Fatalf("no agent:\n%s", s)
	}
	press(e.a, "A")
	if e.a.toast.kind != 2 || e.a.toast.text == "" {
		t.Fatalf("A explains how to start an agent: %q", e.a.toast.text)
	}
}

func TestCopyKeyFromHosts(t *testing.T) {
	e := newTestApp(t, 130, 40)
	a := e.a
	a.hosts.focus("elm")
	press(a, "space")
	a.hosts.focus("oak")
	press(a, "space", "C")
	if a.popup == nil || !strings.Contains(screen(a), "Copy which key to elm, oak?") {
		t.Fatalf("two keys: pick one\n%s", screen(a))
	}
	press(a, "1")
	s := screen(a)
	if a.popup == nil || !strings.Contains(s, "Copy id_ed25519 to elm, oak") || !strings.Contains(s, "Linux/macOS · ~/.ssh/authorized_keys") {
		t.Fatalf("confirmation:\n%s", s)
	}

	k := a.keys.list[0]
	job := a.copyJob(k, []string{"elm", "oak"}, "")
	if len(job.steps) != 2 {
		t.Fatal(job.steps)
	}
	argv := job.steps[1].argv
	if n := len(argv); argv[0] != "ssh" || argv[n-2] != "oak" || !strings.HasPrefix(argv[n-1], "sh -c ") || !strings.Contains(argv[n-1], k.Blob()) {
		t.Fatalf("argv %q", argv)
	}

	// elm worked, oak didn't: elm's login is checked.
	_, cmd := a.Update(copyDoneMsg{key: "id_ed25519", aliases: []string{"elm", "oak"}, errs: []error{nil, errors.New("exit status 255")}})
	if !strings.Contains(a.toast.text, "Copied id_ed25519 to elm; failed on oak") {
		t.Fatalf("toast %q", a.toast.text)
	}
	runCmd(a, cmd)
	if l, ok := a.checks.logins["elm"]; !ok || l.Outcome != probe.OK {
		t.Fatalf("elm's login is checked: %+v", a.checks.logins)
	}
	if _, ok := a.checks.logins["oak"]; ok {
		t.Fatal("oak failed, so it isn't checked")
	}
}

func TestCopyKeyToWindows(t *testing.T) {
	a := newTestApp(t, 130, 40).a
	a.Update(tailscaleMsg{peers: []*tailscale.Peer{{DNSName: "oak.example.ts.net.", OS: "windows", Online: true, LastSeen: time.Now()}}})
	k := a.keys.list[0]
	a.confirmCopy(k, []string{"oak"}, "")
	if !strings.Contains(screen(a), "Windows · administrators_authorized_keys for admins") || !strings.Contains(screen(a), "(Tailscale)") {
		t.Fatalf("detected Windows:\n%s", screen(a))
	}
	if argv := a.copyJob(k, []string{"oak"}, "").steps[0].argv; !strings.HasPrefix(argv[len(argv)-1], "powershell -NoProfile") {
		t.Fatalf("argv %q", argv)
	}
	press(a, "w") // treat as Linux after all
	if !strings.Contains(screen(a), "Linux/macOS") {
		t.Fatalf("w switches:\n%s", screen(a))
	}
	if argv := a.copyJob(k, []string{"oak"}, osUnix).steps[0].argv; !strings.HasPrefix(argv[len(argv)-1], "sh -c") {
		t.Fatalf("argv %q", argv)
	}
}

func TestCopyKeyFromKeysTab(t *testing.T) {
	a := newTestApp(t, 130, 40).a
	press(a, "3", "C")
	if a.tab != tabHosts || a.copyKey == nil || !strings.Contains(screen(a), "Copy id_ed25519 to which hosts?") || !strings.HasPrefix(strings.TrimSpace(bar(a)), "PICK HOSTS") {
		t.Fatalf("the Hosts tab asks for hosts:\n%s", screen(a))
	}
	press(a, "esc")
	if a.copyKey != nil {
		t.Fatal("esc cancels")
	}
	press(a, "3", "C")
	a.hosts.focus("elm")
	press(a, "C")
	if a.popup == nil || !strings.Contains(screen(a), "Copy id_ed25519 to elm") {
		t.Fatalf("C on a host confirms:\n%s", screen(a))
	}
}

func TestNewKeyForm(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	press(a, "3", "n")
	if a.keyForm == nil {
		t.Fatal("n opens the form")
	}
	name := a.keyForm.name.Value()
	if !strings.HasPrefix(name, "id_ed25519_") {
		t.Fatalf("id_ed25519 is taken, so: %q", name)
	}
	press(a, "tab")
	a.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // type: ecdsa
	if keyTypes[a.keyForm.typ] != "ecdsa" || a.keyForm.name.Value() != "id_ecdsa" {
		t.Fatalf("type %s, name %s", keyTypes[a.keyForm.typ], a.keyForm.name.Value())
	}
	a.keyForm.name.SetValue("work")
	press(a, "ctrl+s")
	if a.keyForm == nil || a.keyForm.err != "work already exists" {
		t.Fatalf("err %q", a.keyForm.err)
	}
	if args := keys.GenerateArgs("rsa", "/k", "me@x"); strings.Join(args, " ") != "-t rsa -b 4096 -C me@x -f /k" {
		t.Fatal(args)
	}
}

func TestTerminalJob(t *testing.T) {
	var out bytes.Buffer
	job := &terminalJob{steps: []jobStep{
		{title: "first", argv: []string{"go", "version"}},
		{argv: []string{"go", "no-such-command"}},
	}}
	job.SetStdin(strings.NewReader("\n"))
	job.SetStdout(&out)
	job.SetStderr(&out)
	job.Run()
	if job.errs[0] != nil || job.errs[1] == nil {
		t.Fatalf("errs %v", job.errs)
	}
	if s := out.String(); !strings.Contains(s, "── first ──") || !strings.Contains(s, "Press enter") {
		t.Fatalf("output:\n%s", s)
	}
}

func TestHostMetadata(t *testing.T) {
	e := newTestApp(t, 140, 40)
	a := e.a
	a.hosts.focus("elm")
	press(a, "e")
	a.form.inputs[fTags].SetValue("#Home, lab")
	a.form.inputs[fWeb].SetValue("elm.lan:8080")
	a.form.inputs[fNotes].SetValue("the NAS")
	saveKey(a)
	if a.form != nil {
		t.Fatalf("saved: %s", a.form.err)
	}
	if strings.Contains(readFile(t, e.sshPath), "home") {
		t.Fatal("metadata stays out of the ssh config")
	}
	if !strings.Contains(readFile(t, a.cfgPath), "[hosts.elm]") {
		t.Fatalf("config.toml:\n%s", readFile(t, a.cfgPath))
	}
	s := screen(a)
	for _, want := range []string{"TAGS", "#home #lab", "NOTES", "elm.lan:8080", "the NAS"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	press(a, "/", "#", "l", "a", "b", "enter")
	if rows := a.hosts.hostRows(); len(rows) != 1 || rows[0].host.Alias != "elm" {
		t.Fatalf("#lab filters by tag: %d rows", len(rows))
	}
	press(a, "esc")

	// Renaming carries it over; deleting drops it.
	a.hosts.focus("elm")
	press(a, "e")
	a.form.inputs[fAlias].SetValue("nas")
	saveKey(a)
	if m := a.cfg.Meta("nas"); m.Notes != "the NAS" || !a.cfg.Meta("elm").Empty() {
		t.Fatalf("after rename: %+v / %+v", m, a.cfg.Meta("elm"))
	}
	a.hosts.focus("nas")
	press(a, "e")
	a.form.focus = focusDelete
	press(a, "enter")
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(a, cmd)
	if len(a.cfg.Hosts) != 0 {
		t.Fatalf("deleted host's metadata is gone: %v", a.cfg.Hosts)
	}
}
