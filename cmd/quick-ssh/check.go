package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/probe"
	"github.com/bferg314/quick-ssh/internal/resolve"
	"github.com/bferg314/quick-ssh/internal/run"
	"github.com/bferg314/quick-ssh/internal/sshconfig"
	"github.com/bferg314/quick-ssh/internal/status"
	"github.com/bferg314/quick-ssh/internal/tailscale"
)

// runCheck reports which hosts are up, like the dashboard's status
// column. With --login it also tries key authentication on each. It
// returns exit status 1 when any host is down (or, with --login, can't be
// logged into).
func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	login := fs.Bool("login", false, "also check that key authentication works (connects to each host)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := load()
	if err != nil {
		return err
	}

	var hosts []*sshconfig.Host
	if fs.NArg() == 0 {
		_, hidden := s.cfg.HostMarks()
		for _, h := range s.ssh.Hosts {
			if !hidden[config.HostKey(h.Alias)] {
				hosts = append(hosts, h)
			}
		}
	} else {
		for _, a := range fs.Args() {
			h := s.ssh.Find(a)
			if h == nil {
				return fmt.Errorf("no host %q in %s", a, config.TildePath(s.ssh.Path))
			}
			hosts = append(hosts, h)
		}
	}

	ctx := context.Background()
	r := run.Exec{}
	opts := resolve.Options{Timeout: 10 * time.Second}
	if s.cfg.CustomSSHConfig() {
		opts.ConfigFile = s.cfg.SSHConfigPath()
	}

	// Resolve every host, the jump hosts' included, so "via elm" can use
	// elm's state.
	var mu sync.Mutex
	resolved := map[string]*resolve.Resolved{}
	var aliases []string
	for _, h := range s.ssh.Hosts {
		aliases = append(aliases, h.Alias)
	}
	resolve.All(ctx, r, opts, aliases, 8, func(alias string, res *resolve.Resolved) {
		mu.Lock()
		resolved[config.HostKey(alias)] = res
		mu.Unlock()
	})

	var peers []*tailscale.Peer
	if s.cfg.Tailscale && config.Available("tailscale") {
		tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		peers, _ = tailscale.Status(tctx, r)
		cancel()
	}

	reach := map[string]probe.Reach{}
	logins := map[string]probe.Login{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, h := range s.ssh.Hosts {
		res := resolved[config.HostKey(h.Alias)]
		if res.Err != nil || res.Jump() != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rc := probe.Dial(ctx, res.HostName, res.Port, s.cfg.Timeout())
			mu.Lock()
			reach[config.HostKey(h.Alias)] = rc
			mu.Unlock()
		}()
	}
	if *login {
		loginSem := make(chan struct{}, 4)
		for _, h := range hosts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				loginSem <- struct{}{}
				defer func() { <-loginSem }()
				l := probe.CheckLogin(ctx, r, opts, h.Alias)
				mu.Lock()
				logins[config.HostKey(h.Alias)] = l
				mu.Unlock()
			}()
		}
	}
	wg.Wait()

	var state func(h *sshconfig.Host, depth int) status.State
	state = func(h *sshconfig.Host, depth int) status.State {
		k := config.HostKey(h.Alias)
		in := status.Input{Res: resolved[k]}
		if in.Res.Err == nil {
			in.Peer = tailscale.Match(peers, in.Res.HostName)
		}
		if rc, ok := reach[k]; ok {
			in.Reach = &rc
		}
		if l, ok := logins[k]; ok {
			in.Login = &l
		}
		if in.Res.Err == nil && in.Res.ProxyJump != "" && depth < 5 {
			if j := s.ssh.Find(status.FirstHop(in.Res.ProxyJump)); j != nil {
				js := state(j, depth+1)
				in.Jump = &js
			}
		}
		return status.Of(in)
	}

	failed := false
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, h := range hosts {
		st := state(h, 0)
		dot := map[status.Kind]string{status.Up: "●", status.Down: "○", status.Offline: "○", status.Warn: "▲"}[st.Kind]
		if dot == "" {
			dot = "◌"
		}
		text := st.Text
		if st.Kind == status.Down || st.Kind == status.Offline {
			failed = true
			if text == "" {
				text = "down"
			}
		}
		res := resolved[config.HostKey(h.Alias)]
		target := ""
		if res.Err == nil {
			target = res.Target()
		}
		line := fmt.Sprintf("%s %s\t%s\t%s", dot, h.Alias, text, target)
		if *login {
			l := logins[config.HostKey(h.Alias)]
			mark := "✓ "
			if l.Outcome != probe.OK {
				mark, failed = "✗ ", true
			}
			line += "\t" + mark + l.Outcome.String()
			if l.Outcome != probe.OK && l.Detail != "" {
				line += ": " + strings.TrimSpace(l.Detail)
			}
		}
		fmt.Fprintln(tw, line)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if failed {
		return exitError(1)
	}
	return nil
}
