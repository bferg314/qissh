package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/remote"
)

type factsMsg struct {
	alias string
	facts remote.Facts
}

// fetchFacts (i) asks the selected hosts, or the current one, about
// themselves.
func (a *App) fetchFacts() tea.Cmd {
	aliases := a.targets()
	if len(aliases) == 0 {
		return nil
	}
	r, opts := a.runner, a.sshOptions()
	cmds := []tea.Cmd{a.startSpinner()}
	for _, alias := range aliases {
		a.fetching[config.HostKey(alias)] = true
		windows, _ := a.isWindows(alias, "")
		cmds = append(cmds, func() tea.Msg {
			return factsMsg{alias: alias, facts: remote.GetFacts(context.Background(), r, opts, alias, windows)}
		})
	}
	if len(aliases) > 1 {
		cmds = append(cmds, a.notify(0, "Fetching facts from %d hosts", len(aliases)))
	}
	return tea.Batch(cmds...)
}

func (a *App) handleFacts(m factsMsg) tea.Cmd {
	delete(a.fetching, config.HostKey(m.alias))
	f := m.facts
	if err := a.facts.Put(m.alias, f); err != nil {
		return a.notify(2, "Saving facts: %v", err)
	}
	if len(a.fetching) > 0 {
		return nil
	}
	if f.Err != "" {
		return a.notify(2, "Facts from %s: %s", m.alias, f.Err)
	}
	return a.notify(1, "Facts from %s", m.alias)
}

// appendFacts shows what the host said about itself last time, in lines
// w cells wide.
func (a *App) appendFacts(alias string, w int, add func(string), section func(string)) {
	st := a.st
	section("FACTS")
	if a.fetching[config.HostKey(alias)] {
		add(a.spin.View() + st.dim.Render(" asking the host…"))
	}
	f, ok := a.facts.Get(alias)
	if !ok {
		if !a.fetching[config.HostKey(alias)] {
			add(st.faintText.Render("i fetches the OS, uptime, load, memory, disk and updates"))
		}
		return
	}
	label := func(s string) string { return st.dim.Render(fmt.Sprintf("%-10s", s)) + " " }
	if f.Err != "" {
		add(st.warn.Render(f.Err))
	}
	if f.OS != "" {
		add(label("os") + st.textS.Render(f.OS))
	}
	if f.Kernel != "" {
		k := f.Kernel
		if f.Arch != "" {
			k += " · " + f.Arch
		}
		add(label("kernel") + st.textS.Render(k))
	}
	if f.Uptime > 0 {
		add(label("up") + st.textS.Render(duration(f.Uptime)))
	}
	if f.Load != "" {
		l := f.Load
		if f.CPUs > 0 {
			l += fmt.Sprintf("  (%d %s)", f.CPUs, plural(f.CPUs, "cpu", "cpus"))
		}
		add(label("load") + st.textS.Render(l))
	} else if f.CPUs > 0 {
		add(label("cpus") + st.textS.Render(fmt.Sprint(f.CPUs)))
	}
	if f.MemTotal > 0 {
		add(label("memory") + a.usage(f.MemUsed, f.MemTotal))
	}
	switch {
	case len(f.Disks) > 0:
		// One line each: the mount, then "25% of 4.0 GiB" in a column. A
		// mount too long for the pane loses its start, since the end is
		// what tells drives apart.
		mw, uw := 0, 0
		for _, d := range f.Disks {
			mw = max(mw, len([]rune(d.Mount)))
			uw = max(uw, lipgloss.Width(a.diskUsage(d)))
		}
		mw = max(6, min(mw, w-11-2-uw))
		for i, d := range f.Disks {
			l := label("")
			if i == 0 {
				l = label(plural(len(f.Disks), "disk", "disks"))
			}
			add(l + st.textS.Render(fitLeft(d.Mount, mw)) + "  " + a.diskUsage(d))
		}
	case f.DiskTotal > 0: // facts fetched before every drive was listed
		add(label("disk") + a.usage(f.DiskUsed, f.DiskTotal))
	}
	switch {
	case f.Updates > 0:
		add(label("updates") + st.warn.Render(fmt.Sprintf("%d pending", f.Updates)))
	case f.Updates == 0:
		add(label("updates") + st.ok.Render("up to date"))
	}
	if f.Reboot {
		add(label("reboot") + st.warn.Render("needed"))
	}
	add(st.faintText.Render("as of " + ago(f.At) + " · i refreshes"))
}

// usage is "1.2 of 3.8 GiB (31%)", coloured as it fills up.
func (a *App) usage(used, total int64) string {
	pct := percent(used, total)
	return a.fillStyle(pct).Render(fmt.Sprintf("%s of %s (%d%%)", kib(used), kib(total), pct))
}

// diskUsage is the shorter " 31% of 3.8 GiB" a drive's line has room for.
func (a *App) diskUsage(d remote.Disk) string {
	pct := percent(d.Used, d.Total)
	return a.fillStyle(pct).Render(fmt.Sprintf("%3d%% of %s", pct, kib(d.Total)))
}

func percent(used, total int64) int { return int(used * 100 / max(1, total)) }

// fillStyle colours a usage as it fills up.
func (a *App) fillStyle(pct int) lipgloss.Style {
	switch {
	case pct >= 90:
		return a.st.bad
	case pct >= 75:
		return a.st.warn
	}
	return a.st.textS
}

// fitLeft pads s to w cells, or cuts its start to fit: "…/media files".
func fitLeft(s string, w int) string {
	if r := []rune(s); len(r) > w {
		s = "…" + string(r[len(r)-w+1:])
	}
	return s + strings.Repeat(" ", max(0, w-len([]rune(s))))
}

// kib formats a size given in KiB, with a decimal only below 10: "4.0 GiB",
// "932 GiB".
func kib(n int64) string {
	unit := func(v float64, name string) string {
		if v < 9.95 {
			return fmt.Sprintf("%.1f %s", v, name)
		}
		return fmt.Sprintf("%.0f %s", v, name)
	}
	switch {
	case n >= 1<<30:
		return unit(float64(n)/(1<<30), "TiB")
	case n >= 1<<20:
		return unit(float64(n)/(1<<20), "GiB")
	case n >= 1<<10:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d KiB", n)
}

// duration is "3d 4h", "5h 12m" or "12m".
func duration(d time.Duration) string {
	days, hours, mins := int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
