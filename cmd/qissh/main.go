// Command qissh is a terminal dashboard for the hosts in ~/.ssh/config:
// connect with a key press, see where each host is defined, and edit it.
// The shell integration (qissh init <shell>) runs it as qs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/doctor"
	"github.com/bferg314/qissh/internal/history"
	"github.com/bferg314/qissh/internal/keys"
	"github.com/bferg314/qissh/internal/resolve"
	"github.com/bferg314/qissh/internal/run"
	"github.com/bferg314/qissh/internal/shell"
	"github.com/bferg314/qissh/internal/sshconfig"
	"github.com/bferg314/qissh/internal/ui"
)

var version = "dev"

// exitError carries ssh's exit code out of main.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	cmdFile := flag.String("cmd-file", "", "write the ssh command here instead of running it (used by the shell wrapper)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage:
  qissh                   the dashboard
  qissh <host>            connect: an exact alias or the only fuzzy match, else the dashboard, filtered
  qissh -                 reconnect to the last host
  qissh ls [--json|--names]
                              list hosts
  qissh check [--login] [host...]
                              which hosts are up (exit status 1 if any are down)
  qissh doctor            mistakes in your ssh config (exit status 1 if any need fixing)
  qissh init <shell>      print shell integration (%s), which defines qs

Flags:
`, strings.Join(shell.Names(), ", "))
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := config.MoveOldFolders(); err != nil {
		fmt.Fprintln(os.Stderr, "qissh: moving quick-ssh's folders:", err)
	}

	var err error
	switch {
	case *showVersion:
		fmt.Println("qissh", version)
	case flag.Arg(0) == "init":
		err = runInit(flag.Arg(1))
	case flag.Arg(0) == "ls":
		err = runLs(flag.Args()[1:])
	case flag.Arg(0) == "doctor":
		err = runDoctor()
	case flag.Arg(0) == "check":
		err = runCheck(flag.Args()[1:])
	case flag.NArg() > 1:
		flag.Usage()
		os.Exit(2)
	default:
		err = runMain(flag.Arg(0), *cmdFile)
	}
	var code exitError
	switch {
	case errors.As(err, &code):
		os.Exit(int(code))
	case err != nil:
		fmt.Fprintln(os.Stderr, "qissh:", err)
		os.Exit(1)
	}
}

type setup struct {
	cfg     *config.Config
	cfgPath string
	hist    *history.History
	ssh     *sshconfig.Config
}

func load() (*setup, error) {
	cfgPath, err := config.Path()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	histPath, err := history.DefaultPath()
	if err != nil {
		return nil, err
	}
	ssh, err := sshconfig.Load(cfg.SSHConfigPath())
	if err != nil {
		return nil, err
	}
	return &setup{cfg: cfg, cfgPath: cfgPath, hist: history.Load(histPath), ssh: ssh}, nil
}

func runInit(sh string) error {
	if sh == "" {
		fmt.Fprintln(os.Stderr, "Add one of these to your shell's startup file:")
		for _, n := range []string{"bash", "zsh", "fish", "powershell"} {
			fmt.Fprintln(os.Stderr, "  "+shell.Install(n))
		}
		return fmt.Errorf("missing shell name")
	}
	name := "qs"
	if cfgPath, err := config.Path(); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil {
			name = cfg.AliasName
		}
	}
	s, err := shell.Script(sh, name)
	if err != nil {
		return err
	}
	fmt.Print(s)
	return nil
}

// runMain connects straight away when query picks one host, and otherwise
// opens the dashboard.
func runMain(query, cmdFile string) error {
	s, err := load()
	if err != nil {
		return err
	}
	viaShell := s.cfg.ConnectMode == config.ConnectShell && cmdFile != ""

	if query != "" {
		alias, err := pick(s, query)
		if err != nil {
			return err
		}
		if alias != "" {
			_ = s.hist.Record(alias)
			return finish(ui.Outcome{Argv: ui.SSHArgv(s.cfg, alias), ViaShell: viaShell}, cmdFile)
		}
	}

	app := ui.New(ui.Options{
		Config:     s.cfg,
		ConfigPath: s.cfgPath,
		Runner:     run.Exec{},
		History:    s.hist,
		Filter:     query,
		CmdFile:    cmdFile,
	})
	_, err = tea.NewProgram(app).Run()
	app.StopTunnels() // they're ssh processes qissh started
	if err != nil {
		return err
	}
	return finish(app.Outcome(), cmdFile)
}

// runDoctor prints the config doctor's findings and fails when there's
// a warning or a problem.
func runDoctor() error {
	s, err := load()
	if err != nil {
		return err
	}
	list, _ := keys.Scan(keys.Dir())
	findings := doctor.Check(doctor.Input{Config: s.ssh, Keys: list, Stat: os.Stat, Expand: config.ExpandPath,
		Permissions: runtime.GOOS != "windows"})
	if len(findings) == 0 {
		fmt.Println("✓ nothing to fix in " + config.TildePath(s.ssh.Path))
		return nil
	}
	bad := false
	for _, f := range findings {
		icon := map[doctor.Level]string{doctor.Info: "·", doctor.Warning: "!", doctor.Problem: "✗"}[f.Level]
		fmt.Printf("%s %s\n    %s\n    fix: %s\n", icon, f.Title, f.Detail, f.Fix)
		if f.Line > 0 {
			fmt.Printf("    at %s:%d\n", config.TildePath(f.File), f.Line)
		}
		bad = bad || f.Level >= doctor.Warning
	}
	if bad {
		return exitError(1)
	}
	return nil
}

// pick turns a query into the alias to connect to: "-" is the last host,
// an exact alias wins, then a fuzzy match if it's the only one. Something
// that looks like user@host or a hostname and matches no alias is passed
// to ssh as it is. "" means open the dashboard.
func pick(s *setup, query string) (string, error) {
	if query == "-" {
		if last := s.hist.Last(); last != "" {
			return last, nil
		}
		return "", errors.New("no previous connection to repeat")
	}
	if h := s.ssh.Find(query); h != nil {
		return h.Alias, nil
	}
	names := make([]string, len(s.ssh.Hosts))
	for i, h := range s.ssh.Hosts {
		names[i] = h.Alias
	}
	switch ms := fuzzy.Find(query, names); {
	case len(ms) == 1:
		return names[ms[0].Index], nil
	case len(ms) == 0 && strings.ContainsAny(query, "@.:"):
		return query, nil
	}
	return "", nil
}

// finish runs the ssh command the dashboard chose, or hands it to the
// shell wrapper.
func finish(o ui.Outcome, cmdFile string) error {
	if len(o.Argv) == 0 {
		return nil
	}
	if o.ViaShell {
		return os.WriteFile(cmdFile, []byte(strings.Join(o.Argv, "\n")+"\n"), 0o600)
	}
	fmt.Fprintln(os.Stderr, "→ "+strings.Join(o.Argv, " "))
	cmd := exec.Command(o.Argv[0], o.Argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Ctrl+C belongs to the session, not to qissh waiting for it.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exitError(exit.ExitCode())
	}
	return err
}

// lsHost is one host in `ls --json`.
type lsHost struct {
	Alias         string    `json:"alias"`
	HostName      string    `json:"hostname,omitempty"`
	User          string    `json:"user,omitempty"`
	Port          int       `json:"port,omitempty"`
	ProxyJump     string    `json:"proxy_jump,omitempty"`
	File          string    `json:"file"`
	Line          int       `json:"line"`
	Pinned        bool      `json:"pinned"`
	Hidden        bool      `json:"hidden"`
	Tags          []string  `json:"tags,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	Web           string    `json:"web,omitempty"`
	LastConnected time.Time `json:"last_connected,omitzero"`
	Connections   int       `json:"connections"`
	Error         string    `json:"error,omitempty"`
}

func runLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON, with what ssh resolves for each host")
	names := fs.Bool("names", false, "aliases only, one per line (for completion)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := load()
	if err != nil {
		return err
	}
	if *names {
		for _, h := range s.ssh.Hosts {
			fmt.Println(h.Alias)
		}
		return nil
	}

	opts := resolve.Options{Timeout: 10 * time.Second}
	if s.cfg.CustomSSHConfig() {
		opts.ConfigFile = s.cfg.SSHConfigPath()
	}
	aliases := make([]string, len(s.ssh.Hosts))
	for i, h := range s.ssh.Hosts {
		aliases[i] = h.Alias
	}
	resolved := make([]*resolve.Resolved, len(aliases))
	index := map[string]int{}
	for i, a := range aliases {
		index[a] = i
	}
	resolve.All(context.Background(), run.Exec{}, opts, aliases, 8, func(alias string, r *resolve.Resolved) {
		resolved[index[alias]] = r
	})

	pinned, hidden := s.cfg.HostMarks()
	out := make([]lsHost, len(aliases))
	for i, h := range s.ssh.Hosts {
		r := resolved[i]
		l := lsHost{Alias: h.Alias, File: h.File.Path, Line: h.LineNo(),
			Pinned: pinned[config.HostKey(h.Alias)], Hidden: hidden[config.HostKey(h.Alias)]}
		m := s.cfg.Meta(h.Alias)
		l.Tags, l.Notes, l.Web = m.Tags, m.Notes, m.Web
		if r.Err != nil {
			l.Error = r.Err.Error()
		} else {
			l.HostName, l.User, l.Port, l.ProxyJump = r.HostName, r.User, r.Port, r.ProxyJump
		}
		if e := s.hist.Get(h.Alias); e != nil {
			l.LastConnected, l.Connections = e.Last, e.Count
		}
		out[i] = l
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for i, l := range out {
		target := l.Error
		if target == "" {
			target = resolved[i].Target()
			if j := resolved[i].Jump(); j != "" {
				target += " via " + j
			}
		}
		fmt.Fprintf(tw, "%s\t%s\n", l.Alias, target)
	}
	return tw.Flush()
}
