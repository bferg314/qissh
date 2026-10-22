package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/resolve"
	"github.com/bferg314/qissh/internal/run"
	"github.com/bferg314/qissh/internal/sshconfig"
)

const addUsage = "Usage: qissh add <alias> <user@host[:port]> [-i key] [-J jump]"

// parseAdd reads `add`'s arguments. The flags can go before, between or
// after the alias and target.
func parseAdd(args []string) (sshconfig.HostFields, error) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	key := fs.String("i", "", "IdentityFile: the key to log in with")
	jump := fs.String("J", "", "ProxyJump: the host to go through")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), addUsage)
		fs.PrintDefaults()
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return sshconfig.HostFields{}, err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 2 {
		return sshconfig.HostFields{}, errors.New(addUsage)
	}
	user, host, port, err := sshconfig.ParseTarget(pos[1])
	if err != nil {
		return sshconfig.HostFields{}, err
	}
	return sshconfig.HostFields{Alias: pos[0], HostName: host, User: user, Port: port,
		IdentityFile: *key, ProxyJump: *jump}, nil
}

// runAdd adds a host to the main ssh config, with the same checks and
// safe writer as the dashboard's new-host form.
func runAdd(args []string) error {
	fields, err := parseAdd(args)
	if err != nil {
		return err
	}
	s, err := load()
	if err != nil {
		return err
	}
	opts := resolve.Options{Timeout: 10 * time.Second}
	if s.cfg.CustomSSHConfig() {
		opts.ConfigFile = s.cfg.SSHConfigPath()
	}
	return addHost(os.Stdout, s.ssh, fields, resolve.Validator(run.Exec{}, opts, fields.Alias), config.BackupDir())
}

func addHost(out io.Writer, ssh *sshconfig.Config, fields sshconfig.HostFields, validate sshconfig.Validator, backupDir string) error {
	if e := fields.Check(); e != nil {
		return e
	}
	if other := ssh.Find(fields.Alias); other != nil {
		return fmt.Errorf("%s already exists (%s:%d)", other.Alias, config.TildePath(other.File.Path), other.LineNo())
	}
	file := ssh.MainFile()
	file.AddHost(fields)
	if _, err := sshconfig.Save(file, validate, backupDir); err != nil {
		if errors.Is(err, sshconfig.ErrChanged) {
			return fmt.Errorf("%s changed while qissh was writing it; try again", config.TildePath(file.Path))
		}
		return err
	}

	target := fields.HostName
	if fields.Port != "" && strings.Contains(target, ":") {
		target = "[" + target + "]" // IPv6
	}
	if fields.User != "" {
		target = fields.User + "@" + target
	}
	if fields.Port != "" {
		target += ":" + fields.Port
	}
	fmt.Fprintf(out, "✓ added %s (%s) to %s\n", fields.Alias, target, config.TildePath(file.Path))
	if k := fields.IdentityFile; k != "" {
		if _, err := os.Stat(config.ExpandPath(k)); err != nil {
			fmt.Fprintf(out, "  note: %s doesn't exist yet\n", k)
		}
	}
	return nil
}
