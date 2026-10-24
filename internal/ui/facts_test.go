package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/qissh/internal/remote"
)

func factsLines(a *App, alias string, w int) string {
	var lines []string
	add := func(s string) { lines = append(lines, ansi.Strip(s)) }
	a.appendFacts(alias, w, add, add)
	return strings.Join(lines, "\n")
}

func TestFactsDisks(t *testing.T) {
	a := newTestApp(t, 140, 30).a
	a.facts.Put("oak", remote.Facts{At: time.Now(), Updates: -1, DiskUsed: 1 << 20, DiskTotal: 4 << 20,
		Disks: []remote.Disk{{Mount: "/", Used: 1 << 20, Total: 4 << 20}, {Mount: "/mnt/media", Used: 3 << 20, Total: 40 << 20}}})
	got := factsLines(a, "oak", 60)
	for _, want := range []string{"disks      /           1.0/4.0 GiB  25%", "           /mnt/media  3.0/40 GiB    7%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}

	// A long mount loses its start, so the usage still fits.
	a.facts.Put("elm", remote.Facts{At: time.Now(), Updates: -1,
		Disks: []remote.Disk{{Mount: "/srv/storage/archive/photos", Used: 1 << 20, Total: 4 << 20}}})
	for _, l := range strings.Split(factsLines(a, "elm", 37), "\n") {
		if strings.Contains(l, "%") && (len([]rune(l)) > 37 || !strings.Contains(l, "…/photos  1.0/4.0 GiB  25%")) {
			t.Fatalf("%q doesn't fit 37 cells", l)
		}
	}

	// Facts fetched before every drive was listed still show the root.
	a.facts.Put("box", remote.Facts{At: time.Now(), Updates: -1, DiskUsed: 1 << 20, DiskTotal: 4 << 20})
	if got := factsLines(a, "box", 60); !strings.Contains(got, "disk       1.0/4.0 GiB  25%") {
		t.Fatalf("old facts:\n%s", got)
	}
}

// d cycles the pane beside the list, hidden and full screen; narrow
// terminals skip the side.
func TestDetailCycle(t *testing.T) {
	a := newTestApp(t, 140, 30).a
	for _, want := range []detailLayout{layoutSide, layoutNone, layoutFull, layoutSide} {
		if got := a.detailLayout(); got != want {
			t.Fatalf("layout %d, want %d", got, want)
		}
		press(a, "d")
		press(a, "d")
		press(a, "d") // a full turn comes back
		if got := a.detailLayout(); got != want {
			t.Fatalf("after three presses: %d, want %d", got, want)
		}
		press(a, "d")
	}
	press(a, "d", "d") // full screen
	press(a, "esc")
	if a.detailLayout() != layoutSide {
		t.Fatal("esc leaves full screen for the side pane")
	}

	a.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	for _, want := range []detailLayout{layoutNone, layoutFull, layoutNone, layoutFull} {
		if got := a.detailLayout(); got != want {
			t.Fatalf("narrow: layout %d, want %d", got, want)
		}
		press(a, "d")
	}
}

func TestRatio(t *testing.T) {
	for _, c := range []struct {
		used, total int64
		want        string
	}{
		{442411880, 482797652, "422/460 GiB"},
		{3 << 20, 8 << 20, "3.0/8.0 GiB"},
		{466 << 20, 1863 << 20, "466 GiB/1.8 TiB"}, // used is under a TiB
		{12 << 10, 4 << 30, "12 MiB/4.0 TiB"},
		{2 << 30, 4 << 30, "2.0/4.0 TiB"},
	} {
		if got := ratio(c.used, c.total); got != c.want {
			t.Errorf("ratio(%d, %d) = %q, want %q", c.used, c.total, got, c.want)
		}
	}
}
