// Package doctor looks for mistakes in an ssh config: options that never
// take effect because an earlier block sets them, keys and jump hosts
// that don't exist, hosts defined twice, keys without a passphrase, and
// files ssh refuses because others can write to them.
package doctor

import (
	"fmt"
	"io/fs"
	"net"
	"slices"
	"strings"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/keys"
	"github.com/bferg314/quick-ssh/internal/sshconfig"
)

// Level is how much a finding matters.
type Level int

const (
	Info    Level = iota // worth knowing
	Warning              // probably not what you meant
	Problem              // ssh will fail or refuse
)

func (l Level) String() string { return [...]string{"info", "warning", "problem"}[l] }

// Finding is one thing the doctor noticed.
type Finding struct {
	Level Level
	Title string
	// Detail explains why it matters, and Fix what to do about it.
	Detail, Fix string
	// Where to look: a file and 1-based line, when there is one.
	File string
	Line int
}

// Input is what the doctor checks.
type Input struct {
	Config *sshconfig.Config
	Keys   []*keys.Key
	// Stat and Expand are os.Stat and config.ExpandPath (tests swap them).
	Stat   func(string) (fs.FileInfo, error)
	Expand func(string) string
	// Permissions checks file modes, which ssh enforces on Unix only.
	Permissions bool
}

// accumulate are options ssh collects from every block instead of taking
// the first, so an earlier block doesn't hide them.
var accumulate = map[string]bool{
	"identityfile": true, "certificatefile": true, "localforward": true, "remoteforward": true,
	"dynamicforward": true, "sendenv": true, "include": true,
}

// Check runs every check. Findings come most serious first, then in file
// order.
func Check(in Input) []Finding {
	var out []Finding
	c := in.Config
	add := func(f Finding) { out = append(out, f) }

	for _, w := range c.Warnings {
		add(Finding{Level: Warning, Title: "An Include can't be read", Detail: w,
			Fix: "fix the path or the file's permissions, or remove the Include line"})
	}

	for _, h := range c.Hosts {
		lines := h.Lines()
		lineNo := func(i int) int { return h.LineNo() + i }

		if len(h.Also) > 0 {
			var where []string
			for _, l := range h.Also {
				where = append(where, place(l.File.Path, l.LineNo()))
			}
			add(Finding{Level: Info, Title: fmt.Sprintf("%s is defined more than once", h.Alias),
				Detail: "also at " + strings.Join(where, ", ") + "; for each option ssh uses the first value it reads",
				Fix:    "merge the blocks so there's one place to look", File: h.File.Path, Line: h.LineNo()})
		}

		for i, l := range lines[1:] {
			if l.Keyword == "" || accumulate[l.Keyword] {
				continue
			}
			src, ok := c.Source(h, l.Keyword)
			if !ok || src == h.Location || slices.Contains(h.Also, src) {
				continue
			}
			add(Finding{Level: Warning,
				Title:  fmt.Sprintf("%s's %s is ignored", h.Alias, keyword(l)),
				Detail: fmt.Sprintf("%s at %s sets it first, and ssh uses the first value it reads", src.Text(), place(src.File.Path, src.LineNo())),
				Fix:    fmt.Sprintf("move %s below the hosts (to the end of the file)", src.Text()),
				File:   h.File.Path, Line: lineNo(i + 1)})
		}

		for i, l := range lines[1:] {
			switch l.Keyword {
			case "identityfile":
				if len(l.Args) == 0 || strings.Contains(l.Args[0], "%") || strings.EqualFold(l.Args[0], "none") {
					continue
				}
				path := in.Expand(l.Args[0])
				if _, err := in.Stat(path); err != nil {
					add(Finding{Level: Problem, Title: fmt.Sprintf("%s's IdentityFile doesn't exist", h.Alias),
						Detail: l.Args[0] + " isn't there, so ssh can't offer that key",
						Fix:    "fix the path, or make the key on the Keys tab (n)", File: h.File.Path, Line: lineNo(i + 1)})
				}
			case "proxyjump":
				for _, hop := range jumpHosts(l.Args) {
					if c.Find(hop) == nil {
						add(Finding{Level: Warning, Title: fmt.Sprintf("%s jumps through %s, which isn't a host here", h.Alias, hop),
							Detail: "ssh will look " + hop + " up as a hostname, without its own settings",
							Fix:    "add " + hop + " as a host, or fix the name", File: h.File.Path, Line: lineNo(i + 1)})
					}
				}
			}
		}
	}

	for _, k := range in.Keys {
		if k.Protection == keys.NoPassphrase {
			add(Finding{Level: Info, Title: k.Name + " has no passphrase",
				Detail: "anyone who copies the file can log in with it",
				Fix:    "ssh-keygen -p -f " + config.TildePath(k.Path) + " adds one (the agent saves typing it)", File: k.Path})
		}
	}

	if in.Permissions {
		for _, f := range c.Files {
			if fi, err := in.Stat(f.Path); err == nil && fi.Mode().Perm()&0o022 != 0 {
				add(Finding{Level: Problem, Title: "Others can write to " + config.TildePath(f.Path),
					Detail: "ssh refuses to use a config file other users could change (Bad owner or permissions)",
					Fix:    "chmod 600 " + config.TildePath(f.Path), File: f.Path})
			}
		}
		for _, k := range in.Keys {
			if k.Path == "" {
				continue
			}
			if fi, err := in.Stat(k.Path); err == nil && fi.Mode().Perm()&0o077 != 0 {
				add(Finding{Level: Problem, Title: k.Name + " can be read by others",
					Detail: "ssh ignores a private key other users can read (UNPROTECTED PRIVATE KEY FILE)",
					Fix:    "chmod 600 " + config.TildePath(k.Path), File: k.Path})
			}
		}
	}

	slices.SortStableFunc(out, func(a, b Finding) int { return int(b.Level) - int(a.Level) })
	return out
}

// jumpHosts are the alias-looking hops of a ProxyJump: names without
// dots that aren't addresses, with user@ and :port removed.
func jumpHosts(args []string) []string {
	if len(args) == 0 || strings.EqualFold(args[0], "none") {
		return nil
	}
	var out []string
	for _, hop := range strings.Split(args[0], ",") {
		hop = strings.TrimPrefix(hop, "ssh://")
		if i := strings.LastIndex(hop, "@"); i >= 0 {
			hop = hop[i+1:]
		}
		if h, _, err := net.SplitHostPort(hop); err == nil {
			hop = h
		}
		hop = strings.Trim(hop, "[]")
		if hop == "" || strings.Contains(hop, ".") || net.ParseIP(hop) != nil || strings.EqualFold(hop, "localhost") {
			continue
		}
		out = append(out, hop)
	}
	return out
}

func keyword(l sshconfig.Line) string {
	s := l.KeywordText
	if len(l.Args) > 0 {
		s += " " + strings.Join(l.Args, " ")
	}
	return s
}

func place(path string, line int) string {
	return fmt.Sprintf("%s:%d", config.TildePath(path), line)
}
