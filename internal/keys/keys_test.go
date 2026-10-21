package keys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sshString(b []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(b)))
	return append(out, b...)
}

func ed25519Pub(t *testing.T, comment string) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := append(sshString([]byte("ssh-ed25519")), sshString(pub)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment
}

func rsaPub(t *testing.T, bits int) string {
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	e := []byte{1, 0, 1}
	n := append([]byte{0}, k.N.Bytes()...) // mpint: leading zero keeps it positive
	blob := append(append(sshString([]byte("ssh-rsa")), sshString(e)...), sshString(n)...)
	return "ssh-rsa " + base64.StdEncoding.EncodeToString(blob) + " me@box"
}

// openSSHPrivate is the start of an OpenSSH private key file with the
// given cipher: enough for protection(), with no key in it.
func openSSHPrivate(cipher string) []byte {
	body := append([]byte("openssh-key-v1\x00"), sshString([]byte(cipher))...)
	return pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: body})
}

func TestParsePublic(t *testing.T) {
	line := ed25519Pub(t, "alice@elm laptop")
	typ, bits, fp, comment, err := ParsePublic(line)
	if err != nil || typ != "ED25519" || bits != 256 || comment != "alice@elm laptop" || !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("%s %d %s %q %v", typ, bits, fp, comment, err)
	}
	typ, bits, _, _, err = ParsePublic(rsaPub(t, 1024))
	if err != nil || typ != "RSA" || bits != 1024 {
		t.Fatalf("rsa: %s %d %v", typ, bits, err)
	}
	for _, bad := range []string{"", "ssh-ed25519", "ssh-ed25519 !!!", "ssh-rsa " + strings.Fields(line)[1]} {
		if _, _, _, _, err := ParsePublic(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}

	// The fingerprint is ssh-keygen's.
	if _, err := exec.LookPath("ssh-keygen"); err == nil {
		f := filepath.Join(t.TempDir(), "k.pub")
		os.WriteFile(f, []byte(line+"\n"), 0o600)
		out, err := exec.Command("ssh-keygen", "-l", "-f", f).Output()
		_, _, fp, _, _ := ParsePublic(line)
		if err != nil || !strings.Contains(string(out), fp) {
			t.Fatalf("ssh-keygen says %q (%v), we say %s", out, err, fp)
		}
	}
}

func TestProtection(t *testing.T) {
	cases := map[string]Protection{
		string(openSSHPrivate("none")):       NoPassphrase,
		string(openSSHPrivate("aes256-ctr")): Passphrase,
		"-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,00\n\nAAAA\n-----END RSA PRIVATE KEY-----\n": Passphrase,
		"-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n":                                                     NoPassphrase,
		"-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n":                                         Passphrase,
		"garbage": ProtectionUnknown,
	}
	for in, want := range cases {
		if got := protection([]byte(in)); got != want {
			t.Errorf("%.40q: %v, want %v", in, got, want)
		}
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) { os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600) }
	write("id_ed25519", string(openSSHPrivate("none")))
	write("id_ed25519.pub", ed25519Pub(t, "me@here")+"\n")
	write("work", string(openSSHPrivate("aes256-ctr"))) // no .pub
	write("old.pub", rsaPub(t, 1024))                   // no private key
	write("config", "Host x\n")
	write("known_hosts", "x ssh-ed25519 AAAA\n")
	write("notes.pub", "not a key\n")

	ks, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, k := range ks {
		names = append(names, k.Name)
	}
	if got := strings.Join(names, ","); got != "id_ed25519,old,work" {
		t.Fatalf("found %s", got)
	}
	if k := ks[0]; k.Protection != NoPassphrase || k.Type != "ED25519" || k.Comment != "me@here" || k.Path == "" || k.PubPath == "" {
		t.Fatalf("id_ed25519: %+v", k)
	}
	if k := ks[1]; k.Path != "" || k.Type != "RSA" {
		t.Fatalf("old: %+v", k)
	}
	if k := ks[2]; k.Protection != Passphrase || k.PubPath != "" {
		t.Fatalf("work: %+v", k)
	}
	if ks, err := Scan(filepath.Join(dir, "missing")); err != nil || ks != nil {
		t.Fatal("a missing ~/.ssh has no keys")
	}
}

func TestUsedBy(t *testing.T) {
	home := filepath.Join("C:", "home")
	k := &Key{Path: filepath.Join(home, ".ssh", "id_ed25519")}
	expand := func(s string) string { return strings.Replace(s, "~", home, 1) }
	if !k.UsedBy([]string{"~/.ssh/id_rsa", "~/.ssh/id_ed25519"}, expand) || k.UsedBy([]string{"~/.ssh/id_rsa"}, expand) {
		t.Fatal("UsedBy")
	}
}

type fakeRunner struct {
	out string
	err error
	sc  string
}

func (f fakeRunner) Output(_ context.Context, name string, _ ...string) ([]byte, error) {
	return []byte(f.sc), nil
}
func (f fakeRunner) Combined(context.Context, string, ...string) ([]byte, error) {
	return []byte(f.out), f.err
}

func TestLoadAgent(t *testing.T) {
	a := LoadAgent(context.Background(), fakeRunner{out: "256 SHA256:abc alice@elm (ED25519)\n3072 SHA256:def x (RSA)\n"}, "ssh-add")
	if !a.Running || !a.Has(&Key{Fingerprint: "SHA256:def"}) || a.Has(&Key{Fingerprint: "SHA256:zzz"}) {
		t.Fatalf("%+v", a)
	}
	a = LoadAgent(context.Background(), fakeRunner{out: "The agent has no identities.\n", err: errors.New("exit status 1")}, "ssh-add")
	if !a.Running || len(a.Fingerprints) != 0 {
		t.Fatalf("empty agent: %+v", a)
	}
	a = LoadAgent(context.Background(), fakeRunner{out: "Could not open a connection to your authentication agent.\n", err: errors.New("exit status 2")}, "ssh-add")
	if a.Running || !strings.Contains(a.Err, "Could not open") {
		t.Fatalf("no agent: %+v", a)
	}
}

func TestFreeName(t *testing.T) {
	dir := t.TempDir()
	if n := FreeName(dir, "ed25519", "elm"); n != "id_ed25519" {
		t.Fatal(n)
	}
	os.WriteFile(filepath.Join(dir, "id_ed25519"), nil, 0o600)
	if n := FreeName(dir, "ed25519", "elm"); n != "id_ed25519_elm" {
		t.Fatal(n)
	}
	os.WriteFile(filepath.Join(dir, "id_ed25519_elm.pub"), nil, 0o600)
	if n := FreeName(dir, "ed25519", "elm"); n != "id_ed25519_elm_2" {
		t.Fatal(n)
	}
	if n := FreeName(dir, "ed25519", ""); n != "id_ed25519_2" {
		t.Fatal(n)
	}
}

// TestUnixInstall runs the install command under a real sh with a
// throwaway HOME: it adds the key once, fixes a missing final newline,
// and keeps a comment with quotes intact.
func TestUnixInstall(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	auth := filepath.Join(home, ".ssh", "authorized_keys")
	pub := ed25519Pub(t, `alice's "laptop"`)
	cmdline := UnixCommand(pub)
	// The remote side gets one command line; a shell splits it again.
	runIt := func() string {
		cmd := exec.Command(sh, "-c", cmdline)
		cmd.Env = append(os.Environ(), "HOME="+filepath.ToSlash(home))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	if out := runIt(); !strings.Contains(out, "added the key") {
		t.Fatal(out)
	}
	if out := runIt(); !strings.Contains(out, "already in") {
		t.Fatal(out)
	}
	data, _ := os.ReadFile(auth)
	if string(data) != pub+"\n" {
		t.Fatalf("authorized_keys:\n%q", data)
	}

	other := ed25519Pub(t, "other")
	os.WriteFile(auth, []byte(other), 0o600) // no newline at the end
	runIt()
	data, _ = os.ReadFile(auth)
	if string(data) != other+"\n"+pub+"\n" {
		t.Fatalf("appending after a line without a newline:\n%q", data)
	}
}

func TestWindowsInstall(t *testing.T) {
	ps, err := exec.LookPath("powershell")
	if runtime.GOOS != "windows" || err != nil {
		t.Skip("no Windows PowerShell")
	}
	file := filepath.Join(t.TempDir(), "authorized_keys")
	pub := ed25519Pub(t, "alice's laptop")
	runIt := func() string {
		out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-EncodedCommand", EncodePowerShell(WindowsScript(pub, file))).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	other := ed25519Pub(t, "other")
	os.WriteFile(file, []byte(other), 0o600)
	if out := runIt(); !strings.Contains(out, "added the key") {
		t.Fatal(out)
	}
	if out := runIt(); !strings.Contains(out, "already in") {
		t.Fatal(out)
	}
	data, _ := os.ReadFile(file)
	if string(data) != other+"\r\n"+pub+"\r\n" {
		t.Fatalf("authorized_keys:\n%q", data)
	}
	if c := WindowsCommand(pub); !strings.HasPrefix(c, "powershell -NoProfile -NonInteractive -EncodedCommand ") || strings.ContainsAny(c, `'"`) {
		t.Fatalf("the command needs no quoting: %s", c)
	}
}
