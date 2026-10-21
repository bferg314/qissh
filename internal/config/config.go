// Package config loads and saves quick-ssh's TOML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Tool launch modes.
const (
	// ModeTerminal suspends the TUI and hands the terminal to the tool
	// (sftp, mosh, ...). quick-ssh resumes when it exits.
	ModeTerminal = "terminal"
	// ModeDetach starts the tool in the background and returns immediately
	// (VS Code and other GUI apps).
	ModeDetach = "detach"
)

// Where an SSH session runs; see PLAN.md §5.
const (
	ConnectAuto    = "auto"     // zellij, then Windows Terminal, then in place
	ConnectInPlace = "in_place" // in this terminal
	ConnectZellij  = "zellij"   // a new zellij tab
	ConnectWT      = "wt"       // a new Windows Terminal tab
	ConnectShell   = "shell"    // quit and let the qs wrapper run ssh
)

// ConnectModes lists the modes in the order Settings cycles through them.
var ConnectModes = []string{ConnectAuto, ConnectInPlace, ConnectZellij, ConnectWT, ConnectShell}

// DefaultSSHConfig is the user's ssh config. quick-ssh only passes -F to
// ssh when SSHConfig is set to something else.
const DefaultSSHConfig = "~/.ssh/config"

// Tool is an external program that can be launched for a host.
type Tool struct {
	Name string `toml:"name"`
	Cmd  string `toml:"cmd"`
	// Args may use {alias}, {hostname}, {user}, {port} and {home}.
	Args    []string `toml:"args"`
	Key     string   `toml:"key"`
	Mode    string   `toml:"mode"`
	Enabled bool     `toml:"enabled"`
	// InPlace keeps a terminal tool in quick-ssh's own terminal even
	// inside zellij.
	InPlace bool `toml:"in_place,omitempty"`
	// AutoDisabled marks a tool quick-ssh switched off because its command
	// wasn't installed. It is switched back on once the command appears.
	// Toggling a tool by hand clears it, so user choices stick.
	AutoDisabled bool `toml:"auto_disabled,omitempty"`
}

// Config is the full on-disk configuration.
type Config struct {
	// Version is the config format.
	Version int `toml:"version"`
	// SSHConfig is the ssh config file to read and edit. Supports ~ and
	// environment variables.
	SSHConfig string `toml:"ssh_config"`
	// AliasName is the shell function `quick-ssh init` defines.
	AliasName   string `toml:"alias_name"`
	ConnectMode string `toml:"connect_mode"`
	// StayOpen returns to the list after an in-place session ends,
	// instead of quitting.
	StayOpen bool `toml:"stay_open"`
	// Powerline draws the status bar with arrow separators, which need a
	// Nerd Font or Powerline-patched font.
	Powerline bool `toml:"powerline"`
	// Pinned hosts sort to the top and Hidden ones are left out. Both hold
	// host aliases.
	Pinned []string `toml:"pinned"`
	Hidden []string `toml:"hidden"`
	Tools  []Tool   `toml:"tools"`
}

// Path returns the location of the config file.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "quick-ssh", "config.toml"), nil
}

// configVersion is the current config format.
const configVersion = 1

// Load reads the config at path. If it does not exist, defaults are created,
// installed tools are enabled, and the result is written to disk.
func Load(path string) (*Config, error) {
	cfg := Default()
	_, err := toml.DecodeFile(path, cfg)
	if errors.Is(err, fs.ErrNotExist) {
		cfg.DetectTools()
		return cfg, cfg.Save(path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	cfg.fill()
	if enabled := cfg.EnableInstalled(); len(enabled) > 0 {
		if err := cfg.Save(path); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// fill replaces empty or unknown values with defaults.
func (c *Config) fill() {
	d := Default()
	if c.SSHConfig == "" {
		c.SSHConfig = d.SSHConfig
	}
	if c.AliasName == "" {
		c.AliasName = d.AliasName
	}
	if !slices.Contains(ConnectModes, c.ConnectMode) {
		c.ConnectMode = d.ConnectMode
	}
}

// Save writes the config to path, creating parent directories as needed.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# quick-ssh configuration\n" +
		"# connect_mode: auto, in_place, zellij, wt or shell\n" +
		"# Tool args: {alias}, {hostname}, {user}, {port} and {home} are replaced.\n" +
		"# Tool modes: \"terminal\" (suspends quick-ssh) or \"detach\" (runs in background).\n\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// SSHConfigPath is the expanded path of the ssh config.
func (c *Config) SSHConfigPath() string { return ExpandPath(c.SSHConfig) }

// CustomSSHConfig reports whether ssh needs -F to find the config.
func (c *Config) CustomSSHConfig() bool {
	return !strings.EqualFold(filepath.Clean(c.SSHConfigPath()), filepath.Clean(ExpandPath(DefaultSSHConfig)))
}

// DetectTools enables every configured tool whose command is on PATH and
// marks the rest as auto-disabled, so they switch on once installed.
func (c *Config) DetectTools() {
	for i := range c.Tools {
		ok := Available(c.Tools[i].Cmd)
		c.Tools[i].Enabled, c.Tools[i].AutoDisabled = ok, !ok
	}
}

// EnableInstalled switches on auto-disabled tools whose command is now on
// PATH and returns their names. Tools the user turned off are untouched.
func (c *Config) EnableInstalled() []string {
	var names []string
	for i := range c.Tools {
		t := &c.Tools[i]
		if t.AutoDisabled && !t.Enabled && Available(t.Cmd) {
			t.Enabled, t.AutoDisabled = true, false
			names = append(names, t.Name)
		}
	}
	return names
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Version:     configVersion,
		SSHConfig:   DefaultSSHConfig,
		AliasName:   "qs",
		ConnectMode: ConnectAuto,
		Tools: []Tool{
			{Name: "VS Code", Cmd: "code", Args: []string{"--remote", "ssh-remote+{alias}", "{home}"}, Key: "o", Mode: ModeDetach},
			{Name: "SFTP", Cmd: "sftp", Args: []string{"{alias}"}, Key: "f", Mode: ModeTerminal},
			{Name: "mosh", Cmd: "mosh", Args: []string{"{alias}"}, Key: "M", Mode: ModeTerminal},
		},
	}
}
