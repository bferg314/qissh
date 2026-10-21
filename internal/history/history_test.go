package history

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAndScore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h", "history.json")
	h := Load(path)
	now := time.Now()
	h.RecordAt("Oak", now.Add(-30*24*time.Hour))
	h.RecordAt("oak", now.Add(-30*24*time.Hour))
	h.RecordAt("oak", now.Add(-30*24*time.Hour))
	h.RecordAt("elm", now.Add(-time.Minute))

	if h.Last() != "elm" {
		t.Errorf("last = %q", h.Last())
	}
	if e := h.Get("OAK"); e == nil || e.Count != 3 {
		t.Fatalf("aliases ignore case: %+v", e)
	}
	// Three old visits (3 × 0.25) rank below one from a minute ago (1 × 4).
	if h.Score("elm", now) <= h.Score("oak", now) || h.Score("nobody", now) != 0 {
		t.Errorf("scores elm %v oak %v", h.Score("elm", now), h.Score("oak", now))
	}

	again := Load(path)
	if again.Last() != "elm" || again.Get("oak").Count != 3 {
		t.Fatalf("not saved: %+v", again)
	}
	again.Rename("elm", "elm2")
	if again.Get("elm") != nil || again.Get("elm2") == nil || again.Last() != "elm2" {
		t.Errorf("rename: %+v", again)
	}
}

func TestLoadBadFile(t *testing.T) {
	if h := Load(filepath.Join(t.TempDir(), "missing.json")); len(h.Hosts) != 0 {
		t.Fatal("missing file is empty")
	}
}
