package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/remote"
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

// appendFacts shows what the host said about itself last time.
func (a *App) appendFacts(alias string, add func(string), section func(string)) {
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
	if f.DiskTotal > 0 {
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
	pct := int(used * 100 / max(1, total))
	style := a.st.textS
	switch {
	case pct >= 90:
		style = a.st.bad
	case pct >= 75:
		style = a.st.warn
	}
	return style.Render(fmt.Sprintf("%s of %s (%d%%)", kib(used), kib(total), pct))
}

// kib formats a size given in KiB.
func kib(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f TiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<20))
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
