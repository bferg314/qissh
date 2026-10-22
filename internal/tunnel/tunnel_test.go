package tunnel

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bferg314/qissh/internal/resolve"
)

func TestParse(t *testing.T) {
	ok := map[string]string{
		"5432":                   "L 5432:localhost:5432",
		"8080:80":                "L 8080:localhost:80",
		"8080:db:5432":           "L 8080:db:5432",
		"127.0.0.1:8080:db:5432": "L 127.0.0.1:8080:db:5432",
		"-L 8080:db:5432":        "L 8080:db:5432",
		"l 3000":                 "L 3000:localhost:3000",
		"R 9000:localhost:3000":  "R 9000:localhost:3000",
		"r 9000":                 "R 9000:localhost:9000",
		"D 1080":                 "D 1080",
		"d 127.0.0.1:1080":       "D 127.0.0.1:1080",
		"  5432  ":               "L 5432:localhost:5432",
	}
	for in, want := range ok {
		s, err := Parse(in)
		if err != nil || s.String() != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, s, err, want)
		}
	}
	for _, bad := range []string{"", "x", "99999", "8080:", "X 80", "L 80 90", "D a:b:1080", "a:b:c:d:e", "8080:db:x"} {
		if s, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) = %q, want an error", bad, s)
		}
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]string{
		"5432":                  "localhost:5432 → localhost:5432 from the host",
		"R 9000:localhost:3000": "the host's port 9000 → localhost:3000 from here",
		"D 1080":                "SOCKS proxy on localhost:1080",
	}
	for in, want := range cases {
		s, _ := Parse(in)
		if s.Describe() != want {
			t.Errorf("%s: %q", in, s.Describe())
		}
	}
	for in, want := range map[string]int{"5432": 5432, "127.0.0.1:8080:db:5432": 8080, "D 1080": 1080, "R 9000": 0} {
		if s, _ := Parse(in); s.LocalPort() != want {
			t.Errorf("%s: port %d", in, s.LocalPort())
		}
	}
}

func TestArgs(t *testing.T) {
	s, _ := Parse("5432")
	got := strings.Join(Args(resolve.Options{ConfigFile: "/c"}, "db", s), " ")
	if !strings.HasPrefix(got, "-F /c -N -o BatchMode=yes -o ExitOnForwardFailure=yes") || !strings.HasSuffix(got, "-L 5432:localhost:5432 db") {
		t.Fatal(got)
	}
}

type fakeProc struct{ done chan error }

func (p *fakeProc) Wait() error { return <-p.done }
func (p *fakeProc) Kill() error {
	p.done <- errors.New("killed")
	return nil
}

func TestManager(t *testing.T) {
	var procs []*fakeProc
	var argv []string
	m := NewManager(func(name string, args ...string) (Proc, error) {
		argv = append([]string{name}, args...)
		p := &fakeProc{done: make(chan error, 1)}
		procs = append(procs, p)
		return p, nil
	})
	s, _ := Parse("5432")
	ended := make(chan error, 4)
	done := func(err error) { ended <- err }
	if err := m.Start(resolve.Options{}, "db", "postgres", s, done); err != nil {
		t.Fatal(err)
	}
	if argv[0] != "ssh" || !m.Running("DB", s) || len(m.List()) != 1 || m.List()[0].Name != "postgres" {
		t.Fatalf("%q %v", argv, m.List())
	}
	if err := m.Start(resolve.Options{}, "db", "", s, done); err == nil {
		t.Fatal("it's already running")
	}

	// Stopped on purpose: done gets nil.
	m.Stop("db", s)
	if err := <-ended; err != nil || m.Running("db", s) {
		t.Fatalf("stop: %v", err)
	}

	// ssh gives up by itself: done gets why.
	m.Start(resolve.Options{}, "db", "", s, done)
	procs[1].done <- errors.New("bind [127.0.0.1]:5432: Address already in use")
	select {
	case err := <-ended:
		if err == nil || !strings.Contains(err.Error(), "Address already in use") {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("no end")
	}
	if m.Running("db", s) {
		t.Fatal("it ended")
	}

	s2, _ := Parse("D 1080")
	m.Start(resolve.Options{}, "db", "", s, done)
	m.Start(resolve.Options{}, "web", "", s2, done)
	m.StopAll()
	if len(m.List()) != 0 {
		t.Fatal(m.List())
	}
}

// A real process: its last line of output becomes the error.
func TestExecProc(t *testing.T) {
	p, err := Exec("go", "no-such-command")
	if err != nil {
		t.Skip(err)
	}
	if err := p.Wait(); err == nil || !strings.Contains(err.Error(), "go help") {
		t.Fatalf("%v", err)
	}
}
