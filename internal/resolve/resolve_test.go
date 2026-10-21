package resolve

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const sample = `host oak
user alice
hostname oak.example.ts.net
port 2222
identityfile ~/.ssh/id_rsa
identityfile ~/.ssh/id_ed25519
proxyjump none
proxycommand none
localforward 5432 [localhost]:5432
`

func TestParse(t *testing.T) {
	r := Parse(sample)
	if r.HostName != "oak.example.ts.net" || r.User != "alice" || r.Port != 2222 {
		t.Fatalf("got %+v", r)
	}
	if !reflect.DeepEqual(r.IdentityFiles, []string{"~/.ssh/id_rsa", "~/.ssh/id_ed25519"}) {
		t.Errorf("identity files %q", r.IdentityFiles)
	}
	if r.Jump() != "" {
		t.Errorf("none means no jump: %q", r.Jump())
	}
	if r.Target() != "alice@oak.example.ts.net:2222" {
		t.Errorf("target %q", r.Target())
	}
	if r := Parse("hostname h\nport 22\nproxyjump bastion\n"); r.Target() != "h" || r.Jump() != "bastion" {
		t.Errorf("port 22 is left out of the target: %q, jump %q", r.Target(), r.Jump())
	}
}

type fakeRunner struct {
	mu   sync.Mutex
	args [][]string
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.args = append(f.args, append([]string{name}, args...))
	f.mu.Unlock()
	alias := args[len(args)-1]
	if alias == "broken" {
		return nil, errors.New("bad configuration option")
	}
	return []byte("hostname " + alias + ".lan\nport 22\n"), nil
}

func TestAll(t *testing.T) {
	f := &fakeRunner{}
	var mu sync.Mutex
	got := map[string]*Resolved{}
	All(context.Background(), f, Options{ConfigFile: "/x/config"}, []string{"a", "b", "broken"}, 2, func(alias string, r *Resolved) {
		mu.Lock()
		got[alias] = r
		mu.Unlock()
	})
	if got["a"].HostName != "a.lan" || got["b"].HostName != "b.lan" || got["broken"].Err == nil {
		t.Fatalf("got %+v", got)
	}
	if args := strings.Join(f.args[0][:4], " "); args != "ssh -G -F /x/config" {
		t.Errorf("args %q", args)
	}
}
