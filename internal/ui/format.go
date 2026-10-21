package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// fit pads or truncates a styled string to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// ago formats t relative to now: "just now", "5m ago", "3d ago", ...
func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}

// agoStyle makes recent activity stand out and old activity recede.
func (s styles) agoStyle(t time.Time) lipgloss.Style {
	switch d := time.Since(t); {
	case t.IsZero():
		return s.faintText
	case d < 24*time.Hour:
		return s.ok
	case d < 30*24*time.Hour:
		return s.textS
	default:
		return s.dim
	}
}

// highlight renders name in w cells with fuzzy-matched characters
// highlighted. matches are byte offsets into name.
func (s styles) highlight(name string, w int, matches []int, selected bool) string {
	base := s.textS
	if selected {
		base = s.selName
	}
	if len(matches) == 0 {
		return fit(base.Render(name), w)
	}
	matched := make(map[int]bool, len(matches))
	for _, b := range matches {
		matched[utf8.RuneCountInString(name[:b])] = true
	}
	var b strings.Builder
	for i, r := range []rune(name) {
		if matched[i] {
			b.WriteString(s.match.Render(string(r)))
		} else {
			b.WriteString(base.Render(string(r)))
		}
	}
	return fit(b.String(), w)
}

// plural picks one or many to agree with n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// shorten cuts s to n runes, marking the cut with "…".
func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:max(0, n-1)]) + "…"
	}
	return s
}
