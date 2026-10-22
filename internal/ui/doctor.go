package ui

import (
	"fmt"
	"os"
	"runtime"
	"sort"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/doctor"
)

// runDoctor re-checks the config; it's cheap (a stat per IdentityFile),
// so it runs whenever the config or the keys are read.
func (a *App) runDoctor() {
	a.findings = doctor.Check(doctor.Input{
		Config:      a.ssh,
		Keys:        a.keys.list,
		Stat:        os.Stat,
		Expand:      config.ExpandPath,
		Permissions: runtime.GOOS != "windows",
	})
	if a.doctor != nil {
		a.doctor.cursor = min(a.doctor.cursor, max(0, len(a.findings)-1))
	}
}

// toFix counts the warnings and problems.
func (a *App) toFix() int {
	n := 0
	for _, f := range a.findings {
		if f.Level >= doctor.Warning {
			n++
		}
	}
	return n
}

// doctorView lists the findings (!); enter opens the line in question.
type doctorView struct {
	cursor, offset int
}

func (a *App) doctorKey(key string) tea.Cmd {
	d := a.doctor
	switch key {
	case "esc", "q", "!":
		a.doctor = nil
	case "up", "k":
		d.cursor = max(0, d.cursor-1)
	case "down", "j":
		d.cursor = max(0, min(len(a.findings)-1, d.cursor+1))
	case "enter", "E":
		if d.cursor < len(a.findings) {
			if f := a.findings[d.cursor]; f.Line > 0 {
				a.doctor = nil
				return a.editFile(f.File, f.Line)
			}
		}
	case "y":
		if d.cursor < len(a.findings) {
			return a.copyText("the fix", a.findings[d.cursor].Fix)
		}
	}
	return nil
}

func (a *App) renderDoctor() string {
	st := a.st
	d := a.doctor
	w := max(20, min(96, a.w-6))
	lines := []string{st.bold.Render("Config doctor") + st.dim.Render(" · "+config.TildePath(a.ssh.Path)), ""}
	if len(a.findings) == 0 {
		lines = append(lines, st.ok.Render("✓ Nothing to fix."), "", st.dim.Render("esc closes"))
		return a.boxed(st.box, lines)
	}

	icon := func(l doctor.Level) string {
		switch l {
		case doctor.Problem:
			return st.bad.Render("✗")
		case doctor.Warning:
			return st.warn.Render("!")
		}
		return st.dim.Render("·")
	}
	var body []string
	var starts []int // the body line each finding starts on
	cursorLine := 0
	for i, f := range a.findings {
		marker, title := "  ", st.textS.Render(f.Title)
		if i == d.cursor {
			marker, title = st.marker.Render("▌ "), st.selName.Render(f.Title)
			cursorLine = len(body)
		}
		starts = append(starts, len(body))
		body = append(body, marker+icon(f.Level)+" "+title)
		if i != d.cursor {
			continue
		}
		for _, l := range wrap(f.Detail, w-6) {
			body = append(body, "      "+st.dim.Render(l))
		}
		for j, l := range wrap(f.Fix, w-11) {
			lab := "     "
			if j == 0 {
				lab = "fix: "
			}
			body = append(body, "      "+st.dim.Render(lab)+st.target.Render(l))
		}
		if f.Line > 0 {
			body = append(body, "      "+st.faintText.Render(fmt.Sprintf("%s:%d · enter opens it", config.TildePath(f.File), f.Line)))
		}
	}
	room := max(3, a.h-10)
	if cursorLine < d.offset {
		d.offset = cursorLine
	}
	if cursorLine+5 > d.offset+room {
		d.offset = min(cursorLine, cursorLine+5-room)
	}
	d.offset = max(0, min(d.offset, len(body)-room))
	for j := d.offset; j < min(len(body), d.offset+room); j++ {
		i := sort.SearchInts(starts, j+1) - 1 // the last finding starting at or before j
		a.boxZone(len(lines)+j-d.offset, 0, zone{w: w,
			click: func() tea.Cmd { d.cursor = i; return nil },
			dbl:   func() tea.Cmd { d.cursor = i; return a.doctorKey("enter") },
		})
	}
	lines = append(lines, body[d.offset:min(len(body), d.offset+room)]...)
	lines = append(lines, "", st.dim.Render("↑↓ move · enter opens the line · y copies the fix · esc closes"))
	for i, l := range lines {
		if ansi.StringWidth(l) > w {
			lines[i] = ansi.Truncate(l, w, "…")
		}
	}
	return a.wheelOver(a.boxed(st.box, lines), wheelKeys(a.doctorKey))
}
