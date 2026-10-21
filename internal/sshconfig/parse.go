// Package sshconfig reads ssh config files without losing anything: every
// line is kept exactly as written (indent, keyword spelling, comments, line
// endings), so a file can be edited a line at a time and written back with
// nothing else changed.
//
// It only understands enough of the format to find hosts and edit them.
// What ssh will actually use for a host (defaults, Match, first match wins)
// comes from `ssh -G`, in package resolve.
package sshconfig

import (
	"bytes"
	"strings"
)

// Line is one line of a config file.
type Line struct {
	Text    string   // the line as written, without its line ending
	EOL     string   // "\n", "\r\n", or "" for a last line with no newline
	Indent  string   // leading whitespace
	Keyword string   // lowercased; "" for blank and comment lines
	Args    []string // the values, with quotes removed

	// How the line is written, so it can be rewritten in the same style.
	KeywordText string // the keyword as written, e.g. "HostName" or "hostname"
	Sep         string // between the keyword and the first value: " ", " = ", "\t"...
	Comment     string // a trailing comment with the space before it, or ""
}

// Blank reports whether the line is empty or a comment.
func (l Line) Blank() bool { return l.Keyword == "" }

// Kind is the kind of a block.
type Kind int

const (
	// Preamble is everything before the first Host or Match line.
	Preamble Kind = iota
	HostBlock
	MatchBlock
)

// Block is a run of lines: a Host or Match line and the options under it,
// up to the next Host or Match line.
type Block struct {
	Kind       Kind
	Start, End int // Lines[Start:End]; Start is the Host/Match line itself
}

// File is one parsed config file.
type File struct {
	Path   string
	BOM    bool
	Lines  []Line
	Blocks []Block
	// Orig is the content the file was parsed from. Edits are made on a
	// fresh parse of it, and Save refuses to write if the file on disk no
	// longer matches it.
	Orig []byte
}

var bom = []byte{0xEF, 0xBB, 0xBF}

// Parse splits data into lines and blocks.
func Parse(path string, data []byte) *File {
	f := &File{Path: path, Orig: data}
	if bytes.HasPrefix(data, bom) {
		f.BOM = true
		data = data[len(bom):]
	}
	text := string(data)
	for text != "" {
		var l Line
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			l.Text, l.EOL, text = text[:i], "\n", text[i+1:]
			if strings.HasSuffix(l.Text, "\r") {
				l.Text, l.EOL = l.Text[:len(l.Text)-1], "\r\n"
			}
		} else {
			l.Text, text = text, ""
		}
		parseLine(&l)
		f.Lines = append(f.Lines, l)
	}
	f.index()
	return f
}

// Copy is a fresh parse of the file's original content, to edit without
// touching f.
func (f *File) Copy() *File { return Parse(f.Path, f.Orig) }

// index works out the blocks from the lines.
func (f *File) index() {
	f.Blocks = f.Blocks[:0]
	cur := Block{Kind: Preamble}
	for i, l := range f.Lines {
		var k Kind
		switch l.Keyword {
		case "host":
			k = HostBlock
		case "match":
			k = MatchBlock
		default:
			continue
		}
		cur.End = i
		if cur.Kind != Preamble || cur.End > cur.Start {
			f.Blocks = append(f.Blocks, cur)
		}
		cur = Block{Kind: k, Start: i}
	}
	cur.End = len(f.Lines)
	if cur.Kind != Preamble || cur.End > cur.Start {
		f.Blocks = append(f.Blocks, cur)
	}
}

// Bytes is the file as it would be written: unchanged lines come back
// byte for byte.
func (f *File) Bytes() []byte {
	var b bytes.Buffer
	if f.BOM {
		b.Write(bom)
	}
	for _, l := range f.Lines {
		b.WriteString(l.Text)
		b.WriteString(l.EOL)
	}
	return b.Bytes()
}

// parseLine splits l.Text the way ssh's readconf does: a keyword, then
// whitespace or a single "=", then arguments.
func parseLine(l *Line) {
	s := l.Text
	rest := strings.TrimLeft(s, " \t")
	l.Indent = s[:len(s)-len(rest)]
	if rest == "" || rest[0] == '#' {
		return
	}
	end := strings.IndexAny(rest, " \t=")
	if end < 0 {
		l.KeywordText, l.Keyword = rest, strings.ToLower(rest)
		return
	}
	l.KeywordText, l.Keyword = rest[:end], strings.ToLower(rest[:end])
	after := rest[end:]
	args := strings.TrimLeft(after, " \t")
	if strings.HasPrefix(args, "=") {
		args = strings.TrimLeft(args[1:], " \t")
	}
	l.Sep = after[:len(after)-len(args)]
	var comment int
	l.Args, comment = splitArgs(args)
	if comment >= 0 {
		// Keep the whitespace before the comment with it.
		start := len(strings.TrimRight(args[:comment], " \t"))
		l.Comment = args[start:]
	}
}

// splitArgs is ssh's argv_split: arguments are separated by whitespace,
// may be quoted with " or ', and an unquoted # at the start of an
// argument starts a comment (its index is returned, or -1). A backslash
// escapes a quote, a backslash or (outside quotes) a space; any other
// backslash is kept, so Windows paths such as C:\Users\me\.ssh\id_ed25519
// read as written.
func splitArgs(s string) ([]string, int) {
	var args []string
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			return args, -1
		}
		if s[i] == '#' {
			return args, i
		}
		var arg strings.Builder
		var quote byte
		for ; i < len(s); i++ {
			c := s[i]
			if quote == 0 && (c == ' ' || c == '\t') {
				break
			}
			if c == '\\' && i+1 < len(s) {
				n := s[i+1]
				if n == '\'' || n == '"' || n == '\\' || (quote == 0 && n == ' ') {
					arg.WriteByte(n)
					i++
					continue
				}
			}
			switch {
			case quote == 0 && (c == '"' || c == '\''):
				quote = c
			case quote != 0 && c == quote:
				quote = 0
			default:
				arg.WriteByte(c)
			}
		}
		args = append(args, arg.String())
	}
}

// IsPattern reports whether a Host pattern matches more than one name
// (*, ?) or excludes names (!), so it isn't a host you can connect to.
func IsPattern(p string) bool {
	return strings.ContainsAny(p, "*?") || strings.HasPrefix(p, "!")
}
