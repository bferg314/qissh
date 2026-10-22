package sshconfig

import (
	"fmt"
	"strconv"
	"strings"
)

// Edits change as little of the file as they can: a changed value rewrites
// its one line in the style it was written (keyword spelling, "=" or
// spaces, trailing comment); a new option is added at the end of the
// block's options with the block's indent; nothing else moves.

// Canonical is the usual spelling of the keywords qissh writes.
var Canonical = map[string]string{
	"host":         "Host",
	"hostname":     "HostName",
	"user":         "User",
	"port":         "Port",
	"identityfile": "IdentityFile",
	"proxyjump":    "ProxyJump",
}

// HostFields are the options the new-host and edit forms manage. Other
// options in a block are left as they are.
type HostFields struct {
	Alias, HostName, User, Port, IdentityFile, ProxyJump string
}

// Options lists the fields by keyword, in the order they're written.
func (h HostFields) Options() [][2]string {
	return [][2]string{
		{"hostname", h.HostName},
		{"user", h.User},
		{"port", h.Port},
		{"identityfile", h.IdentityFile},
		{"proxyjump", h.ProxyJump},
	}
}

// FieldsOf reads the form's fields from a host's own block.
func FieldsOf(h *Host) HostFields {
	return HostFields{
		Alias:        h.Alias,
		HostName:     h.Value("hostname"),
		User:         h.Value("user"),
		Port:         h.Value("port"),
		IdentityFile: h.Value("identityfile"),
		ProxyJump:    h.Value("proxyjump"),
	}
}

// Field names one of HostFields' fields.
type Field int

const (
	FieldAlias Field = iota
	FieldHostName
	FieldUser
	FieldPort
	FieldIdentityFile
	FieldProxyJump
)

// FieldError is what's wrong with one field.
type FieldError struct {
	Field Field
	Msg   string
}

func (e *FieldError) Error() string { return e.Msg }

// Check finds the first field qissh wouldn't write: an alias that's empty,
// has spaces or quotes, or is a pattern; spaces in a value; a port that
// isn't 1 to 65535. Whether the alias is already taken is the caller's to
// check.
func (h HostFields) Check() *FieldError {
	bad := func(f Field, msg string) *FieldError { return &FieldError{f, msg} }
	switch {
	case h.Alias == "":
		return bad(FieldAlias, "Give the host an alias")
	case strings.ContainsAny(h.Alias, " \t\"'#"):
		return bad(FieldAlias, "An alias can't contain spaces, quotes or #")
	case IsPattern(h.Alias):
		return bad(FieldAlias, "An alias can't contain * ? or start with ! (those make a pattern)")
	case strings.ContainsAny(h.HostName, " \t"):
		return bad(FieldHostName, "A hostname can't contain spaces")
	case strings.ContainsAny(h.User, " \t"):
		return bad(FieldUser, "A user name can't contain spaces")
	case strings.ContainsAny(h.ProxyJump, " \t"):
		return bad(FieldProxyJump, "ProxyJump is a host alias or user@host:port, without spaces")
	}
	if h.Port != "" {
		if n, err := strconv.Atoi(h.Port); err != nil || n < 1 || n > 65535 {
			return bad(FieldPort, "Port is a number from 1 to 65535")
		}
	}
	return nil
}

// ParseTarget splits user@host:port, where the user and port are
// optional. An IPv6 address takes a port only in brackets ([::1]:2222);
// without them every colon belongs to the address.
func ParseTarget(s string) (user, host, port string, err error) {
	host = s
	if i := strings.LastIndex(s, "@"); i >= 0 {
		user, host = s[:i], s[i+1:]
		if user == "" {
			return "", "", "", fmt.Errorf("%q: nothing before the @", s)
		}
	}
	switch {
	case strings.HasPrefix(host, "["):
		end := strings.Index(host, "]")
		if end < 0 {
			return "", "", "", fmt.Errorf("%q: no ] after the address", s)
		}
		rest := host[end+1:]
		host = host[1:end]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return "", "", "", fmt.Errorf("%q: expected :port after ]", s)
			}
			port = rest[1:]
			if port == "" {
				return "", "", "", fmt.Errorf("%q: no port after the :", s)
			}
		}
	case strings.Count(host, ":") == 1:
		host, port, _ = strings.Cut(host, ":")
		if port == "" {
			return "", "", "", fmt.Errorf("%q: no port after the :", s)
		}
	}
	if host == "" {
		return "", "", "", fmt.Errorf("%q: no host name", s)
	}
	return user, host, port, nil
}

// SetHost makes the block (an index into f.Blocks, a Host block naming
// alias) match fields: the alias is renamed on the Host line, each option
// is changed, added or (when empty) removed.
func (f *File) SetHost(block int, alias string, fields HostFields) error {
	if block < 0 || block >= len(f.Blocks) || f.Blocks[block].Kind != HostBlock {
		return fmt.Errorf("block %d isn't a Host block", block)
	}
	if fields.Alias != alias {
		header := &f.Lines[f.Blocks[block].Start]
		args := append([]string{}, header.Args...)
		found := false
		for i, a := range args {
			if strings.EqualFold(a, alias) {
				args[i], found = fields.Alias, true
			}
		}
		if !found {
			return fmt.Errorf("%s isn't on the Host line at line %d", alias, f.Blocks[block].Start+1)
		}
		f.rewrite(f.Blocks[block].Start, args)
	}
	for _, o := range fields.Options() {
		f.setOption(block, o[0], o[1])
	}
	return nil
}

// setOption sets keyword to value in the block, or removes it when value
// is "". Only the first line with the keyword is changed; repeats (a
// second IdentityFile, say) are removed when the value is cleared and
// otherwise left alone.
func (f *File) setOption(block int, keyword, value string) {
	b := f.Blocks[block]
	var at []int
	for i := b.Start + 1; i < b.End; i++ {
		if f.Lines[i].Keyword == keyword {
			at = append(at, i)
		}
	}
	switch {
	case value == "" && len(at) > 0:
		for j := len(at) - 1; j >= 0; j-- {
			f.removeLines(at[j], at[j]+1)
		}
	case value == "":
	case len(at) > 0:
		if l := f.Lines[at[0]]; len(l.Args) != 1 || l.Args[0] != value {
			f.rewrite(at[0], []string{value})
		}
	default:
		f.insertLines(f.optionsEnd(block), f.newLine(block, keyword, value))
	}
}

// optionsEnd is where a new option goes: after the block's last option,
// before the blank and comment lines that end it (those usually belong to
// the next block).
func (f *File) optionsEnd(block int) int {
	b := f.Blocks[block]
	end := b.End
	for end > b.Start+1 && f.Lines[end-1].Blank() {
		end--
	}
	return end
}

// newLine writes keyword value in the block's style.
func (f *File) newLine(block int, keyword, value string) Line {
	b := f.Blocks[block]
	indent, lower := "    ", f.lowercase(block)
	for i := b.Start + 1; i < b.End; i++ {
		if !f.Lines[i].Blank() {
			indent = f.Lines[i].Indent
			break
		}
	}
	kw := Canonical[keyword]
	if lower {
		kw = keyword
	}
	l := Line{Text: indent + kw + " " + quote(value), EOL: f.eol()}
	parseLine(&l)
	return l
}

// lowercase reports whether the block (or, for an empty block, the file)
// writes keywords in lowercase, as some hand-written configs do.
func (f *File) lowercase(block int) bool {
	lower, other := 0, 0
	count := func(from, to int) {
		for _, l := range f.Lines[from:to] {
			switch {
			case l.Keyword == "":
			case l.KeywordText == l.Keyword:
				lower++
			default:
				other++
			}
		}
	}
	if block >= 0 {
		b := f.Blocks[block]
		count(b.Start, b.End)
	}
	if lower+other <= 1 {
		count(0, len(f.Lines))
	}
	return lower > other
}

// rewrite gives line i new arguments, keeping its indent, keyword
// spelling, separator and trailing comment.
func (f *File) rewrite(i int, args []string) {
	l := &f.Lines[i]
	sep := l.Sep
	if sep == "" {
		sep = " "
	}
	quoted := make([]string, len(args))
	for j, a := range args {
		quoted[j] = quote(a)
	}
	l.Text = l.Indent + l.KeywordText + sep + strings.Join(quoted, " ") + l.Comment
	parseLine(l)
}

// AddHost writes a new Host block before the first pattern Host block or
// Match block (ssh takes the first value it finds for each option, so a
// host placed after Host * would lose to its defaults), or at the end.
func (f *File) AddHost(fields HostFields) {
	at := len(f.Lines)
	for _, b := range f.Blocks {
		if b.Kind == MatchBlock || b.Kind == HostBlock && allPatterns(f.Lines[b.Start].Args) {
			at = b.Start
			// Above the comments that introduce that block.
			for at > 0 && f.Lines[at-1].Keyword == "" && strings.TrimSpace(f.Lines[at-1].Text) != "" {
				at--
			}
			break
		}
	}

	lower := f.lowercase(-1)
	kw := func(k string) string {
		if lower {
			return k
		}
		return Canonical[k]
	}
	eol := f.eol()
	mk := func(text string) Line {
		l := Line{Text: text, EOL: eol}
		parseLine(&l)
		return l
	}
	lines := []Line{mk(kw("host") + " " + quote(fields.Alias))}
	for _, o := range fields.Options() {
		if o[1] != "" {
			lines = append(lines, mk("    "+kw(o[0])+" "+quote(o[1])))
		}
	}

	// One blank line between blocks.
	if at > 0 && strings.TrimSpace(f.Lines[at-1].Text) != "" {
		lines = append([]Line{mk("")}, lines...)
	}
	if at < len(f.Lines) {
		lines = append(lines, mk(""))
	}
	if at == len(f.Lines) && at > 0 && f.Lines[at-1].EOL == "" {
		f.Lines[at-1].EOL = eol // the old last line had no newline
	}
	f.insertLines(at, lines...)
}

func allPatterns(args []string) bool {
	for _, a := range args {
		if !IsPattern(a) {
			return false
		}
	}
	return len(args) > 0
}

// RemoveHost takes alias off the block's Host line. When it's the only
// name there, the whole block goes, with the comment lines directly above
// it.
func (f *File) RemoveHost(block int, alias string) error {
	if block < 0 || block >= len(f.Blocks) || f.Blocks[block].Kind != HostBlock {
		return fmt.Errorf("block %d isn't a Host block", block)
	}
	b := f.Blocks[block]
	header := f.Lines[b.Start]
	var keep []string
	for _, a := range header.Args {
		if !strings.EqualFold(a, alias) {
			keep = append(keep, a)
		}
	}
	switch {
	case len(keep) == len(header.Args):
		return fmt.Errorf("%s isn't on the Host line at line %d", alias, b.Start+1)
	case len(keep) > 0:
		f.rewrite(b.Start, keep)
		return nil
	}
	start := b.Start
	for start > 0 && f.Lines[start-1].Keyword == "" && strings.TrimSpace(f.Lines[start-1].Text) != "" {
		start--
	}
	if start == 0 {
		start = b.Start // comments that open the file are its header, not this host's
	}
	atEnd, lastEOL := b.End == len(f.Lines), f.Lines[len(f.Lines)-1].EOL
	f.removeLines(start, b.End)
	if atEnd {
		// Don't leave the file ending in the blank line that separated it.
		for n := len(f.Lines); n > 0 && strings.TrimSpace(f.Lines[n-1].Text) == ""; n-- {
			f.Lines = f.Lines[:n-1]
		}
		if n := len(f.Lines); n > 0 && lastEOL == "" {
			f.Lines[n-1].EOL = ""
		}
		f.index()
	}
	return nil
}

func (f *File) insertLines(at int, lines ...Line) {
	f.Lines = append(f.Lines[:at], append(lines, f.Lines[at:]...)...)
	f.index()
}

func (f *File) removeLines(from, to int) {
	f.Lines = append(f.Lines[:from], f.Lines[to:]...)
	f.index()
}

// eol is the file's line ending: the first one it uses, or "\n".
func (f *File) eol() string {
	for _, l := range f.Lines {
		if l.EOL != "" {
			return l.EOL
		}
	}
	return "\n"
}

// quote writes an argument so ssh reads it back unchanged: in double
// quotes when it has spaces, quotes or starts with #.
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'") && !strings.HasPrefix(s, "#") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\' && (i+1 == len(s) || s[i+1] == '"' || s[i+1] == '\\'):
			b.WriteString(`\\`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
