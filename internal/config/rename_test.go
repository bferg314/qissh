package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveOldFolder(t *testing.T) {
	parent := t.TempDir()
	old := filepath.Join(parent, "quick-ssh")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "config.toml"), []byte("version = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveOldFolder(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "qissh", "config.toml")); err != nil {
		t.Fatal("the config moved:", err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("the old folder is gone")
	}

	// A second run, or one with no old folder, does nothing.
	if err := moveOldFolder(parent); err != nil {
		t.Fatal(err)
	}
	if err := moveOldFolder(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestMoveOldFolderKeepsNewOne(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"quick-ssh", "qissh"} {
		if err := os.MkdirAll(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := moveOldFolder(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "quick-ssh")); err != nil {
		t.Fatal("the old folder is left alone when the new one exists")
	}
}
