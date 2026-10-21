package config

import "strings"

// HostKey normalizes an alias so the same host always compares equal. ssh
// lowercases the host it's given before matching, so aliases ignore case.
func HostKey(alias string) string { return strings.ToLower(alias) }

// HostMarks returns the keys (see HostKey) of the pinned and hidden hosts.
func (c *Config) HostMarks() (pinned, hidden map[string]bool) {
	return keySet(c.Pinned), keySet(c.Hidden)
}

// SetPinned pins or unpins alias. Pinning a hidden host unhides it.
func (c *Config) SetPinned(alias string, on bool) {
	c.Pinned = setMark(c.Pinned, alias, on)
	if on {
		c.Hidden = setMark(c.Hidden, alias, false)
	}
}

// SetHidden hides or unhides alias. Hiding a pinned host unpins it.
func (c *Config) SetHidden(alias string, on bool) {
	c.Hidden = setMark(c.Hidden, alias, on)
	if on {
		c.Pinned = setMark(c.Pinned, alias, false)
	}
}

func keySet(aliases []string) map[string]bool {
	m := make(map[string]bool, len(aliases))
	for _, a := range aliases {
		m[HostKey(a)] = true
	}
	return m
}

// setMark adds alias to list, or removes every entry for it.
func setMark(list []string, alias string, on bool) []string {
	key := HostKey(alias)
	out := list[:0:0]
	for _, a := range list {
		if HostKey(a) != key {
			out = append(out, a)
		}
	}
	if on {
		out = append(out, alias)
	}
	return out
}
