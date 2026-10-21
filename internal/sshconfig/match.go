package sshconfig

import "strings"

// Matches reports whether a Host line's patterns match name, as ssh's
// match_pattern_list does: case-insensitive globs with * and ?, where a
// matching negated pattern (!pat) rules the name out whatever else matches.
func Matches(patterns []string, name string) bool {
	name = strings.ToLower(name)
	matched := false
	for _, p := range patterns {
		p = strings.ToLower(p)
		neg := strings.HasPrefix(p, "!")
		if neg {
			p = p[1:]
		}
		if glob(p, name) {
			if neg {
				return false
			}
			matched = true
		}
	}
	return matched
}

// glob matches s against a pattern of literal characters, * (any run)
// and ? (any one character).
func glob(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for len(p) > 0 && p[0] == '*' {
				p = p[1:]
			}
			if p == "" {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if glob(p, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
		default:
			if s == "" || s[0] != p[0] {
				return false
			}
		}
		p, s = p[1:], s[1:]
	}
	return s == ""
}

// Source finds the block ssh takes keyword (lowercase) from for h: the
// first block, in reading order, that applies to h and sets it. found is
// false when no block that's known to apply sets it, so ssh uses its
// default. Match blocks other than "Match all" depend on things only ssh
// can check (exec, the network, the user), so they're skipped.
func (c *Config) Source(h *Host, keyword string) (loc Location, found bool) {
	for _, l := range c.Blocks {
		header := l.Header()
		switch header.Keyword {
		case "host":
			if !Matches(header.Args, h.Alias) {
				continue
			}
		case "match":
			if len(header.Args) != 1 || !strings.EqualFold(header.Args[0], "all") {
				continue
			}
		}
		for _, line := range l.Lines()[1:] {
			if line.Keyword == keyword {
				return l, true
			}
		}
	}
	return Location{}, false
}
