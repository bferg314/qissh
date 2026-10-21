package probe

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bferg314/quick-ssh/internal/resolve"
)

func TestDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if r := Dial(context.Background(), "127.0.0.1", port, time.Second); !r.Up || r.Err != "" {
		t.Fatalf("listening port: %+v", r)
	}
	ln.Close()
	if r := Dial(context.Background(), "127.0.0.1", port, time.Second); r.Up || r.Err == "" {
		t.Fatalf("closed port: %+v", r)
	}
}

func TestShortNetErr(t *testing.T) {
	cases := map[string]string{
		"dial tcp 10.0.0.1:22: connectex: No connection could be made because the target machine actively refused it.": "refused",
		"dial tcp 10.0.0.1:22: i/o timeout":                     "timeout",
		"dial tcp: lookup nope.lan: no such host":               "no dns",
		"dial tcp 10.9.9.9:22: connect: network is unreachable": "unreachable",
		"something else": "down",
	}
	for in, want := range cases {
		if got := ShortNetErr(errors.New(in)); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

// exitErr makes a real *exec.ExitError with the given code.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	var cmd []string
	if runtime.GOOS == "windows" {
		cmd = []string{"cmd", "/c", "exit", itoa(code)}
	} else {
		cmd = []string{"sh", "-c", "exit " + itoa(code)}
	}
	_, err := Exec(cmd...)
	return err
}

func TestClassify(t *testing.T) {
	ssh := exitErr(t, 255)
	changed := `@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!
The fingerprint for the ED25519 key sent by the remote host is
SHA256:abc.
Host key for oak.lan has changed and you have requested strict checking.
Host key verification failed.`
	cases := []struct {
		out  string
		err  error
		want Outcome
	}{
		{"", nil, OK},
		{"'exit' is weird here", exitErr(t, 1), OK}, // the remote command failed, the login didn't
		{changed, ssh, HostKeyChanged},
		{"No ED25519 host key is known for oak.lan and you have requested strict checking.\r\nHost key verification failed.\r\n", ssh, HostKeyUnknown},
		{"alice@oak.lan: Permission denied (publickey,password).", ssh, NeedsAuth},
		{"ssh: connect to host oak.lan port 22: Connection refused", ssh, Refused},
		{"ssh: connect to host 10.0.0.9 port 22: Connection timed out", ssh, Timeout},
		{"ssh: Could not resolve hostname nope.lan: Name or service not known", ssh, NoDNS},
		{"kex_exchange_identification: read: Connection reset by peer", ssh, Failed},
	}
	for _, c := range cases {
		l := Classify(c.out, c.err)
		if l.Outcome != c.want {
			t.Errorf("%q: %v, want %v", firstWords(c.out), l.Outcome, c.want)
		}
	}
	if l := Classify(changed, ssh); l.Detail != "Host key verification failed." {
		t.Errorf("detail %q", l.Detail)
	}
}

func firstWords(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}

func TestKnownHostsNameAndFile(t *testing.T) {
	r := resolve.Parse("hostname OAK.lan\nport 22\nuserknownhostsfile /c/Users/me/.ssh/known_hosts /c/Users/me/.ssh/known_hosts2\n")
	if n := knownHostsName(r); n != "oak.lan" {
		t.Errorf("name %q", n)
	}
	r2 := resolve.Parse("hostname h\nport 2222\nhostkeyalias box\n")
	if n := knownHostsName(r2); n != "[box]:2222" {
		t.Errorf("name %q", n)
	}
	want := "/c/Users/me/.ssh/known_hosts"
	if runtime.GOOS == "windows" {
		want = filepath.Clean("C:/Users/me/.ssh/known_hosts")
	}
	if f := KnownHostsFile(r); f != want {
		t.Errorf("file %q, want %q", f, want)
	}
}

// fakeSSH plays ssh and ssh-keygen for ScanHostKey and Trust.
type fakeSSH struct {
	calls [][]string
}

func (f *fakeSSH) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch {
	case args[0] == "-l" && args[1] == "-f": // fingerprint the scanned keys
		return []byte("256 SHA256:NEWKEY oak.lan (ED25519)\n"), nil
	case args[0] == "-l" && args[1] == "-F": // look up the old key
		return []byte("# Host oak.lan found: line 3\noak.lan ED25519 SHA256:OLDKEY\n"), nil
	case args[0] == "-R":
		return nil, nil
	}
	return nil, errors.New("unexpected")
}

func (f *fakeSSH) Combined(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	for i, a := range args {
		if a == "-o" && strings.HasPrefix(args[i+1], "UserKnownHostsFile=") {
			os.WriteFile(strings.TrimPrefix(args[i+1], "UserKnownHostsFile="), []byte("oak.lan ssh-ed25519 AAAANEW\n"), 0o600)
		}
	}
	return []byte("Warning: Permanently added 'oak.lan' (ED25519) to the list of known hosts.\n"), nil
}

func TestScanAndTrust(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(kh, []byte("other ssh-ed25519 AAAAOTHER"), 0o600) // no final newline
	res := resolve.Parse("hostname oak.lan\nport 22\nuserknownhostsfile " + filepath.ToSlash(kh) + "\n")
	if runtime.GOOS == "windows" {
		res.Options["userknownhostsfile"] = []string{kh}
	}
	f := &fakeSSH{}
	hk, err := ScanHostKey(context.Background(), f, resolve.Options{}, "oak", res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(hk.New, ",") != "SHA256:NEWKEY" || strings.Join(hk.Old, ",") != "SHA256:OLDKEY" || hk.Name != "oak.lan" {
		t.Fatalf("scan: %+v", hk)
	}
	if !strings.Contains(strings.Join(f.calls[0], " "), "StrictHostKeyChecking=accept-new") {
		t.Fatalf("scan must accept into a throwaway file: %q", f.calls[0])
	}

	if err := Trust(context.Background(), f, resolve.Options{}, hk); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if strings.Join(last[:3], " ") != "ssh-keygen -R oak.lan" {
		t.Fatalf("old key removed with ssh-keygen -R: %q", last)
	}
	data, _ := os.ReadFile(kh)
	if string(data) != "other ssh-ed25519 AAAAOTHER\noak.lan ssh-ed25519 AAAANEW\n" {
		t.Fatalf("known_hosts:\n%q", data)
	}
}
