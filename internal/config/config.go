// Package config loads and saves qissh's TOML configuration.
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
	"time"

	"github.com/BurntSushi/toml"
)

// Tool launch modes.
const (
	// ModeTerminal suspends the TUI and hands the terminal to the tool
	// (sftp, ...). qissh resumes when it exits.
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

// DefaultSSHConfig is the user's ssh config. qissh only passes -F to
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
	// InPlace keeps a terminal tool in qissh's own terminal even
	// inside zellij.
	InPlace bool `toml:"in_place,omitempty"`
	// AutoDisabled marks a tool qissh switched off because its command
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
	// AliasName is the shell function `qissh init` defines.
	AliasName   string `toml:"alias_name"`
	ConnectMode string `toml:"connect_mode"`
	// StayOpen returns to the list after an in-place session ends,
	// instead of quitting.
	StayOpen bool `toml:"stay_open"`
	// CheckInterval is how often hosts are re-checked while the dashboard
	// is open ("60s"); "0" checks only at start and on r.
	CheckInterval string `toml:"check_interval"`
	// CheckTimeout is how long a reachability check waits ("2s").
	CheckTimeout string `toml:"check_timeout"`
	// Tailscale uses `tailscale status` for hosts on your tailnet.
	Tailscale bool `toml:"tailscale"`
	// Powerline draws the status bar with arrow separators, which need a
	// Nerd Font or Powerline-patched font.
	Powerline bool `toml:"powerline"`
	// Mouse lets the mouse click, double-click and scroll. While it's on,
	// selecting text in the terminal needs Shift (Option on macOS).
	Mouse bool `toml:"mouse"`
	// Pinned hosts sort to the top and Hidden ones are left out. Both hold
	// host aliases.
	Pinned []string `toml:"pinned"`
	Hidden []string `toml:"hidden"`
	// DiscoverHidden are devices hidden on the Discover tab, by their key
	// ("tailnet:oak.example.ts.net", "known:box.lan").
	DiscoverHidden []string `toml:"discover_hidden,omitempty"`
	// Hosts holds tags, notes and a web page per host, keyed by alias.
	Hosts map[string]*HostMeta `toml:"hosts,omitempty"`
	Tools []Tool               `toml:"tools"`
	// Snippets are saved commands for running on hosts.
	Snippets []Snippet `toml:"snippets,omitempty"`
}

// Path returns the location of the config file.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "qissh", "config.toml"), nil
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
	dropped := cfg.dropOldTools()
	if enabled := cfg.EnableInstalled(); len(enabled) > 0 || dropped {
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
	if _, err := time.ParseDuration(c.CheckInterval); err != nil && c.CheckInterval != "0" {
		c.CheckInterval = d.CheckInterval
	}
	if t, err := time.ParseDuration(c.CheckTimeout); err != nil || t <= 0 {
		c.CheckTimeout = d.CheckTimeout
	}
}

// Save writes the config to path, creating parent directories as needed.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# qissh configuration\n" +
		"# connect_mode: auto, in_place, zellij, wt or shell\n" +
		"# Tool args: {alias}, {hostname}, {user}, {port} and {home} are replaced.\n" +
		"# Tool modes: \"terminal\" (suspends qissh) or \"detach\" (runs in background).\n\n")
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
		Version:       configVersion,
		SSHConfig:     DefaultSSHConfig,
		AliasName:     "qs",
		ConnectMode:   ConnectAuto,
		CheckInterval: "60s",
		CheckTimeout:  "2s",
		Tailscale:     true,
		Mouse:         true,
		Tools: []Tool{
			{Name: "VS Code", Cmd: "code", Args: []string{"--remote", "ssh-remote+{alias}", "{home}"}, Key: "o", Mode: ModeDetach},
			{Name: "SFTP", Cmd: "sftp", Args: []string{"{alias}"}, Key: "f", Mode: ModeTerminal},
		},
	}
}

// droppedTools are tools qissh used to add by default. A config still
// holding one exactly as it was added loses it on load; one that was
// changed by hand is kept.
var droppedTools = []Tool{
	{Name: "mosh", Cmd: "mosh", Args: []string{"{alias}"}, Key: "M", Mode: ModeTerminal},
}

// dropOldTools removes droppedTools from the config, reporting whether
// any were there.
func (c *Config) dropOldTools() bool {
	n := len(c.Tools)
	c.Tools = slices.DeleteFunc(c.Tools, func(t Tool) bool {
		return slices.ContainsFunc(droppedTools, func(d Tool) bool {
			return t.Name == d.Name && t.Cmd == d.Cmd && slices.Equal(t.Args, d.Args) &&
				t.Key == d.Key && t.Mode == d.Mode && !t.InPlace
		})
	})
	return len(c.Tools) < n
}

// Interval is CheckInterval as a duration; 0 means no repeat checks.
func (c *Config) Interval() time.Duration {
	d, _ := time.ParseDuration(c.CheckInterval)
	return max(0, d)
}

// Timeout is CheckTimeout as a duration.
func (c *Config) Timeout() time.Duration {
	if d, err := time.ParseDuration(c.CheckTimeout); err == nil && d > 0 {
		return d
	}
	return 2 * time.Second
}

// CheckIntervals are the choices Settings cycles through.
var CheckIntervals = []string{"30s", "60s", "5m", "0"}
