package sshconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// maxDepth is how deep Includes may nest, as in ssh (READCONF_MAX_DEPTH).
const maxDepth = 16

// Config is a config file and everything it includes.
type Config struct {
	Path  string
	Files []*File // the main file first, then included files in the order ssh reads them
	// Hosts are the names you can connect to, in the order ssh reads them.
	Hosts []*Host
	// Rules are the Host blocks with only patterns (Host *, Host *.lan)
	// and the Match blocks: they apply to hosts rather than being hosts.
	Rules []*Rule
	// Blocks are every Host and Match block, in the order ssh reads them.
	Blocks []Location
	// Warnings are included files that couldn't be read.
	Warnings []string

	byKey map[string]*Host
}

// Location is a block in a file.
type Location struct {
	File  *File
	Block int // index into File.Blocks
}

// Header is the block's Host or Match line.
func (l Location) Header() Line { return l.File.Lines[l.File.Blocks[l.Block].Start] }

// LineNo is the 1-based line number of the block's header.
func (l Location) LineNo() int { return l.File.Blocks[l.Block].Start + 1 }

// Lines are the block's lines, header included.
func (l Location) Lines() []Line {
	b := l.File.Blocks[l.Block]
	return l.File.Lines[b.Start:b.End]
}

// Host is one name from a Host line that isn't a pattern.
type Host struct {
	Alias string
	Location
	// Siblings are the other names on the same Host line, which share
	// this block.
	Siblings []string
	// Also lists later blocks that name this host too. ssh applies them
	// as well, for options not set earlier.
	Also []Location
}

// Get returns the arguments of the first line in the host's own block
// that sets keyword (lowercase).
func (h *Host) Get(keyword string) ([]string, bool) {
	for _, l := range h.Lines()[1:] {
		if l.Keyword == keyword {
			return l.Args, true
		}
	}
	return nil, false
}

// Value is Get's first argument, or "".
func (h *Host) Value(keyword string) string {
	if args, ok := h.Get(keyword); ok && len(args) > 0 {
		return args[0]
	}
	return ""
}

// Rule is a Host block of patterns or a Match block.
type Rule struct {
	Location
}

// Text is the block's header as written, without indent or comment,
// such as "Host *" or "Match host *.lan exec ...".
func (l Location) Text() string {
	h := l.Header()
	kw := strings.TrimLeft(h.Text, " \t")[:len(h.Keyword)]
	return strings.TrimSpace(kw + " " + strings.Join(h.Args, " "))
}

// Load reads the config at path and every file it includes. A missing
// main file is an empty config, as it is for ssh.
func Load(path string) (*Config, error) {
	c := &Config{Path: path, byKey: map[string]*Host{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	c.read(Parse(path, data), 0, map[string]bool{})
	return c, nil
}

func (c *Config) read(f *File, depth int, seen map[string]bool) {
	seen[canonical(f.Path)] = true
	c.Files = append(c.Files, f)
	for bi, b := range f.Blocks {
		loc := Location{File: f, Block: bi}
		header := f.Lines[b.Start]
		if b.Kind != Preamble {
			c.Blocks = append(c.Blocks, loc)
		}
		switch b.Kind {
		case MatchBlock:
			c.Rules = append(c.Rules, &Rule{loc})
		case HostBlock:
			var names []string
			for _, p := range header.Args {
				if !IsPattern(p) {
					names = append(names, p)
				}
			}
			if len(names) == 0 {
				c.Rules = append(c.Rules, &Rule{loc})
			}
			for _, n := range names {
				key := strings.ToLower(n)
				if h := c.byKey[key]; h != nil {
					h.Also = append(h.Also, loc)
					continue
				}
				h := &Host{Alias: n, Location: loc}
				for _, s := range names {
					if s != n {
						h.Siblings = append(h.Siblings, s)
					}
				}
				c.byKey[key] = h
				c.Hosts = append(c.Hosts, h)
			}
		}

		// Includes are read where they appear, like ssh does.
		for _, l := range f.Lines[b.Start:b.End] {
			if l.Keyword != "include" {
				continue
			}
			for _, inc := range l.Args {
				c.include(inc, depth, seen)
			}
		}
	}
}

func (c *Config) include(pattern string, depth int, seen map[string]bool) {
	if depth+1 > maxDepth {
		c.Warnings = append(c.Warnings, fmt.Sprintf("Include %s: nested more than %d deep", pattern, maxDepth))
		return
	}
	paths, err := filepath.Glob(includePath(pattern))
	if err != nil {
		c.Warnings = append(c.Warnings, fmt.Sprintf("Include %s: %v", pattern, err))
		return
	}
	sort.Strings(paths)
	for _, p := range paths {
		if seen[canonical(p)] {
			continue
		}
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			c.Warnings = append(c.Warnings, fmt.Sprintf("Include %s: %v", p, err))
			continue
		}
		c.read(Parse(p, data), depth+1, seen)
	}
}

// includePath expands ~ and environment variables. Relative paths are in
// ~/.ssh, as ssh does for a user's config, wherever the including file is.
func includePath(p string) string {
	home, _ := os.UserHomeDir()
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = home + p[1:]
	}
	p = os.ExpandEnv(p)
	if !filepath.IsAbs(p) && !strings.HasPrefix(p, "/") {
		p = filepath.Join(home, ".ssh", p)
	}
	return filepath.FromSlash(p)
}

func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

// Find returns the host with alias, ignoring case as ssh does.
func (c *Config) Find(alias string) *Host {
	for _, h := range c.Hosts {
		if strings.EqualFold(h.Alias, alias) {
			return h
		}
	}
	return nil
}
