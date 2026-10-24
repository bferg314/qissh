package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/qissh/internal/remote"
)

func factsLines(a *App, alias string) string {
	var lines []string
	add := func(s string) { lines = append(lines, ansi.Strip(s)) }
	a.appendFacts(alias, add, add)
	return strings.Join(lines, "\n")
}

func TestFactsDisks(t *testing.T) {
	a := newTestApp(t, 140, 30).a
	a.facts.Put("oak", remote.Facts{At: time.Now(), Updates: -1, DiskUsed: 1 << 20, DiskTotal: 4 << 20,
		Disks: []remote.Disk{{Mount: "/", Used: 1 << 20, Total: 4 << 20}, {Mount: "/mnt/media", Used: 3 << 20, Total: 4 << 20}}})
	got := factsLines(a, "oak")
	for _, want := range []string{"disks      /           1.0 GiB of 4.0 GiB (25%)", "           /mnt/media  3.0 GiB of 4.0 GiB (75%)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}

	// Facts fetched before every drive was listed still show the root.
	a.facts.Put("elm", remote.Facts{At: time.Now(), Updates: -1, DiskUsed: 1 << 20, DiskTotal: 4 << 20})
	if got := factsLines(a, "elm"); !strings.Contains(got, "disk       1.0 GiB of 4.0 GiB (25%)") {
		t.Fatalf("old facts:\n%s", got)
	}
}
