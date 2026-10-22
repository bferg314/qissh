// Package history records which hosts you connect to and when, for the
// default sort order, "last connected" and `qs -`.
package history

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is one host's history.
type Entry struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// History is every host's entry, keyed by lowercase alias.
type History struct {
	path    string
	Hosts   map[string]*Entry `json:"hosts"`
	LastKey string            `json:"last"` // the alias most recently connected to, as typed
}

// DefaultPath is history.json in the OS cache folder.
func DefaultPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "qissh", "history.json"), nil
}

// Load reads the history at path. A missing or unreadable file is an
// empty history: it only affects sorting.
func Load(path string) *History {
	h := &History{path: path, Hosts: map[string]*Entry{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, h)
	}
	if h.Hosts == nil {
		h.Hosts = map[string]*Entry{}
	}
	return h
}

// Get returns alias's entry, or nil.
func (h *History) Get(alias string) *Entry { return h.Hosts[strings.ToLower(alias)] }

// Last is the alias most recently connected to.
func (h *History) Last() string { return h.LastKey }

// Record notes a connection to alias now and saves.
func (h *History) Record(alias string) error {
	return h.RecordAt(alias, time.Now())
}

// RecordAt notes a connection to alias at t and saves.
func (h *History) RecordAt(alias string, t time.Time) error {
	key := strings.ToLower(alias)
	e := h.Hosts[key]
	if e == nil {
		e = &Entry{}
		h.Hosts[key] = e
	}
	e.Count++
	e.Last = t
	h.LastKey = alias
	return h.save()
}

// Rename moves alias's entry to a new name.
func (h *History) Rename(from, to string) error {
	fk, tk := strings.ToLower(from), strings.ToLower(to)
	if e := h.Hosts[fk]; e != nil && fk != tk {
		h.Hosts[tk] = e
		delete(h.Hosts, fk)
	}
	if strings.EqualFold(h.LastKey, from) {
		h.LastKey = to
	}
	return h.save()
}

// Score ranks hosts by frecency: how often, weighted by how recently,
// like zoxide. Hosts never connected to score 0.
func (h *History) Score(alias string, now time.Time) float64 {
	e := h.Get(alias)
	if e == nil {
		return 0
	}
	var w float64
	switch d := now.Sub(e.Last); {
	case d < time.Hour:
		w = 4
	case d < 24*time.Hour:
		w = 2
	case d < 7*24*time.Hour:
		w = 0.5
	default:
		w = 0.25
	}
	return float64(e.Count) * w
}

func (h *History) save() error {
	if h.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.path), "history-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), h.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
