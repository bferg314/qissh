package doctor

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/keys"
	"github.com/bferg314/qissh/internal/sshconfig"
)

const cfg = `Host *
    User root
    ServerAliveInterval 30
    IdentityFile ~/.ssh/default_key

Host oak
    HostName oak.lan
    User alice
    IdentityFile ~/.ssh/missing_key
    IdentityFile ~/.ssh/id_%r
    ProxyJump me@bastion:2222,elm,10.0.0.1,gw.example.com

Host elm
    HostName elm.lan
    IdentityFile ~/.ssh/present_key

Host elm
    Port 2200
`

type fakeInfo struct{ mode fs.FileMode }

func (f fakeInfo) Name() string       { return "" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	os.WriteFile(path, []byte(cfg), 0o600)
	c, err := sshconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]fs.FileMode{"~/.ssh/present_key": 0o600, path: 0o666, "/k/loose": 0o644}
	in := Input{
		Config: c,
		Keys: []*keys.Key{
			{Name: "loose", Path: "/k/loose", Protection: keys.NoPassphrase},
			{Name: "safe", Path: "/k/safe", Protection: keys.Passphrase},
		},
		Expand: func(s string) string { return s },
		Stat: func(p string) (fs.FileInfo, error) {
			if m, ok := modes[p]; ok {
				return fakeInfo{m}, nil
			}
			return nil, fs.ErrNotExist
		},
		Permissions: true,
	}
	var got []string
	for _, f := range Check(in) {
		got = append(got, f.Level.String()+": "+f.Title)
	}
	want := []string{
		"problem: oak's IdentityFile doesn't exist",
		"problem: Others can write to " + config.TildePath(path),
		"problem: loose can be read by others",
		"warning: oak's User alice is ignored",
		"warning: oak jumps through bastion, which isn't a host here",
		"info: elm is defined more than once",
		"info: loose has no passphrase",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	f := Check(in)[3]
	if f.Line != 8 || !strings.Contains(f.Detail, "Host * at ") || !strings.Contains(f.Detail, ":1 sets it first") {
		t.Fatalf("%+v", f)
	}

	in.Permissions = false
	if n := len(Check(in)); n != 5 {
		t.Fatalf("without permission checks: %d", n)
	}
}

func TestJumpHosts(t *testing.T) {
	got := jumpHosts([]string{"ssh://me@a:22,[b]:2,c.lan,1.2.3.4,localhost,d"})
	if strings.Join(got, ",") != "a,b,d" {
		t.Fatal(got)
	}
	if jumpHosts([]string{"none"}) != nil {
		t.Fatal("none")
	}
}
