package config

import (
	"slices"
	"strings"
)

// HostMeta is what quick-ssh keeps about a host that ssh config has no
// place for.
type HostMeta struct {
	Tags  []string `toml:"tags,omitempty"`
	Notes string   `toml:"notes,omitempty"`
	// Web is the host's web page, opened with w.
	Web string `toml:"web,omitempty"`
}

// Empty reports whether there's nothing to keep.
func (m HostMeta) Empty() bool { return len(m.Tags) == 0 && m.Notes == "" && m.Web == "" }

// Meta is alias's metadata. Entries match the alias ignoring case, like
// ssh does.
func (c *Config) Meta(alias string) HostMeta {
	for k, m := range c.Hosts {
		if HostKey(k) == HostKey(alias) && m != nil {
			return *m
		}
	}
	return HostMeta{}
}

// SetMeta replaces alias's metadata; empty metadata removes the entry.
func (c *Config) SetMeta(alias string, m HostMeta) {
	for k := range c.Hosts {
		if HostKey(k) == HostKey(alias) {
			delete(c.Hosts, k)
		}
	}
	if m.Empty() {
		return
	}
	if c.Hosts == nil {
		c.Hosts = map[string]*HostMeta{}
	}
	c.Hosts[alias] = &m
}

// ParseTags splits "home, pi #lab" into tags: separated by commas or
// spaces, without a leading #, lowercase, each once.
func ParseTags(s string) []string {
	var tags []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		t = strings.ToLower(strings.TrimLeft(t, "#"))
		if t != "" && !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	return tags
}
