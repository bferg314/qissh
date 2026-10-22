# QiSSH design

A terminal dashboard for the hosts in `~/.ssh/config`: see which are up, connect with one key, and add, edit or remove them without opening the file. Written in Go with the same stack, look and conventions as [folgit](https://github.com/bferg314/folgit), and launched as `qs`.

```
qs                # the dashboard
qs oak            # connect straight away (exact or unique fuzzy match), else open the dashboard filtered to "oak"
qs -              # reconnect to the last host
```

This is the plan QiSSH is being built from, kept up to date with what's built and the decisions made along the way (see [Decisions](#decisions)). [README.md](README.md) describes how to use it. **Status: M0 to M6 done; M7 next.**

## 1. Goals and non-goals

**Goals**

- From any shell, `qs` → a couple of keystrokes → an SSH session. Faster than remembering aliases.
- One screen that answers "which of my machines are up, and how do I get to each of them?"
- Add, edit, duplicate and delete hosts in `~/.ssh/config` **without losing anything already in the file**: comments, blank lines, ordering, indentation, keyword case and line endings all survive.
- `~/.ssh/config` stays the source of truth. `ssh <alias>` keeps working exactly as before; QiSSH never connects in a way plain `ssh` wouldn't.
- Works the same on Windows (built-in OpenSSH), macOS and Linux.

**Non-goals (v1)**

- An SSH client of its own. QiSSH always runs the system `ssh`, so agents, `ProxyJump`, `ControlMaster`, FIDO keys and anything else in your config just work.
- Storing passwords or secrets. Auth is keys and agents, as with plain `ssh`.
- Editing `Match` blocks or `Host *` defaults through forms. They're shown, and `E` opens the file in your editor at the right line.
- Replacing a file manager or terminal multiplexer. Transfers and tabs hand off to existing tools.

## 2. What comes from folgit

Separate repo; code is copied, not shared, as with dockgit.

| folgit package | Use in QiSSH |
|---|---|
| `internal/ui/styles.go` | As is: palette, light/dark detection, tab styles. |
| `internal/ui/statusbar.go` | Modes become `HOSTS`, `DISCOVER`, `KEYS`, `SETTINGS`, `FILTER`, `EDIT`, `RUN`. Right side: hosts up/down, running tunnels, position, `? help`. |
| `internal/ui/format.go`, `help.go`, `popup.go` | The `?` overlay, action popups, toasts, `fit` and time formatting. |
| detail pane (`detail.go`) | Beside the list at ≥100 columns, full screen when narrower; `J`/`K`/`ctrl+d`/`ctrl+u` scroll; `d` toggles. |
| `internal/config` | `ExpandPath`, `~` and env var handling, versioned TOML, pinned/hidden marks. New schema (§8). |
| `internal/cache` | Not copied: no startup cache was needed (Decisions, 1). Connection history has its own small package. |
| `internal/launcher` | Tools (detach / terminal), zellij tabs, `OpenURL`. Adds Windows Terminal tabs. |
| `internal/shell` | `qissh init <shell>`, now defining `qs` plus completion of host aliases (§6). |

Same dependencies: `charm.land/bubbletea/v2`, `bubbles/v2`, `lipgloss/v2`, `BurntSushi/toml`, `sahilm/fuzzy`. No SSH library: everything goes through the `ssh`, `ssh-keygen`, `ssh-add` and (optionally) `tailscale` commands behind one `Runner` interface, so the UI is tested with a fake, exactly as dockgit does with `docker`.

Same key conventions: `j`/`k` move, `/` filters, `r` refreshes, `s` sorts, `?` help, `q` quits, `1`–`4`/`tab` switch tabs, `*` pins, `h`/`H` hide and show hidden, `y` copies.

## 3. Reading and writing `~/.ssh/config`

This is the core of the project and the part most worth getting right.

### Two views of the same file

1. **The file as written:** a lossless parser (`internal/sshconfig`) turns the file into blocks (a preamble, then `Host` and `Match` blocks), each line kept raw with its indent, keyword spelling (`HostName`, `hostname`, `Hostname` all appear in real files), `key value` vs `key=value` form, quoting, trailing comment and line ending. Writing an unchanged tree back is byte-identical; that's a test, not a hope.
2. **What ssh will actually use:** `ssh -G <alias>` for each host, run 8 at a time and cached against the files' modification times. This is authoritative: it applies `Host *` defaults, `Match` blocks, `Include`s, first-match-wins ordering and `%h`-style tokens, so QiSSH never has to reimplement ssh's rules to know the effective `HostName`, `User`, `Port`, `IdentityFile` or `ProxyJump`.

The detail pane shows the effective values and marks the ones inherited from elsewhere (`Host *`, a `Match` block, an included file).

### What counts as a device

- Each non-wildcard pattern on a `Host` line is a row. `Host web web.prod` gives two rows sharing one block; editing either says so.
- Wildcard and negated patterns (`Host *`, `Host *.lan`, `Host !bastion`) and `Match` blocks aren't devices. They're listed under a **Defaults & rules** section at the bottom of the Hosts tab, read-only, with `E` to edit them in your editor.
- `Include` is followed (globs, paths relative to `~/.ssh`), recursively with a depth limit. Each host remembers the file that defines it, shown in the detail pane, and edits go to that file.

### Writing

- **Minimal edits.** Changing a value rewrites that one line; adding a key appends a line to the block using the block's indent and the file's keyword style; removing a host removes its block and the comment lines directly above it.
- **Placement of new hosts.** ssh uses the *first* value it finds for each key, so a new host goes before the first `Host *` or `Match` block (or at the end if there is none), never after defaults that would override it.
- **Safe replacement.** Write to a temp file in the same folder → validate with `ssh -F <temp> -G <alias>` → keep a backup (last 10 versions in QiSSH's cache folder, plus `config.qs-bak` beside the file) → rename over the original. If validation fails nothing is replaced and the error from ssh is shown.
- **Concurrent edits.** If the file changed on disk since it was read (mtime + hash), the edit is refused and the list reloads, as folgit does before deleting a repo.
- **Windows permissions.** OpenSSH for Windows rejects a config that other users can write to. The temp file is created in `.ssh` so it inherits the folder's ACL; after the rename, `ssh -G` is run once more and the backup is restored if it fails.
- **Line endings** follow the file (CRLF stays CRLF). A brand-new file uses LF.

## 4. Status: is it up, and can I log in?

Three levels, cheapest first. Background checks **never** prompt: anything run by ssh uses `BatchMode=yes`.

1. **Reachability (automatic).** A TCP connect to the effective `HostName:Port`, 8 at a time with a 2 s timeout, on start and every 60 s while the dashboard is open. Shows `●` up with latency, `○` down, `◌` checking. Hosts reached through `ProxyJump`/`ProxyCommand` can't be dialled directly: they show their jump host's state and `via <jump>`.
2. **Tailscale (automatic, if installed).** `tailscale status --json` gives online state, OS and *last seen* for every tailnet peer. A host whose `HostName` matches a peer's MagicDNS name or Tailscale IP uses this instead of dialling, so an offline Pi shows "offline, last seen 3d ago" rather than just a timeout. (6 of the 10 hosts in the current config are `*.ts.net`.)
3. **Login probe (on demand, `t`).** `ssh -o BatchMode=yes -o ConnectTimeout=5 <alias> true`, with the outcome classified from the exit code and stderr:
   - **ok**: key auth works;
   - **needs a password or passphrase** (an interactive connect will still work);
   - **unknown host key**;
   - **host key changed**: offers to remove the old key (`ssh-keygen -R`, which also writes `known_hosts.old`) and connect, showing the new fingerprint first;
   - **refused / timeout / DNS failure**, with the line from ssh.

## 5. Connecting

`enter` runs `ssh <alias>`. No extra options are added, so the session is exactly what typing `ssh <alias>` gives.

Where the session runs (`connect_mode`, default `auto`):

| Mode | Behaviour |
|---|---|
| `in_place` | QiSSH suspends (`tea.ExecProcess`) and the session takes over the terminal. When it ends, QiSSH **quits** (it's a launcher), unless `stay_open = true`, in which case you're back in the list with a toast if ssh exited with an error. |
| `zellij` | Inside zellij: a new tab named after the host, QiSSH stays open (folgit's `ZellijTab`). |
| `wt` | Inside Windows Terminal (`WT_SESSION` set): `wt -w 0 nt --title <alias> ssh <alias>`, QiSSH stays open. |
| `shell` | QiSSH exits and the `qs` wrapper runs `ssh <alias>` in your own shell, so it lands in shell history. Uses a `--cmd-file` the way folgit's `g` uses `--cwd-file`. |

`auto` picks `zellij`, then `wt`, then `in_place`. With several hosts selected (`space`), `enter` opens one tab each (zellij and wt only).

Each connection is recorded (alias and time) in the cache, which powers "last connected", `qs -`, and the default sort: pinned, then frecency (frequent and recent first).

## 6. CLI and the `qs` alias

The binary is `qissh` (so `go install …/cmd/qissh@latest` works and nothing collides with an existing `qs`); the shell integration defines `qs`:

```sh
eval "$(qissh init bash)"                                # ~/.bashrc
eval "$(qissh init zsh)"                                 # ~/.zshrc
qissh init fish | source                                 # ~/.config/fish/config.fish
Invoke-Expression (& qissh init powershell | Out-String)  # $PROFILE
```

Each script defines `qs` (wrapping `qissh` for `connect_mode = "shell"`) and **tab completion of host aliases** for `qs`, using `qissh ls --names`. `alias_name = "qs"` in the config changes the name if `qs` clashes with something.

| Command | Does |
|---|---|
| `qs` | the dashboard |
| `qs <query>` | an exact alias, or a single fuzzy match, connects immediately; otherwise opens the dashboard with the filter set |
| `qs -` | reconnect to the last host |
| `qs ls [--json] [--up] [--tag t] [--names]` | scriptable list (names for completion, JSON for prompts and scripts) |
| `qs check [alias…]` | reachability and login probe without the TUI; non-zero exit if any host is down |
| `qs add <alias> <user@host[:port]> [-i key] [-J jump]` | add a host from the command line, through the same safe writer |
| `qs init <shell>` | shell integration |

The dotfiles' `shared/gup/gup.json` gets a `qissh` entry beside folgit, and the shell blocks that set up folgit's integration add QiSSH's.

## 7. Tabs

### Tab 1: Hosts

Columns: status (`●`/`○`/`◌`, latency) · alias (★ pinned) · `user@hostname[:port]` · tags · last connected. Below 72 columns tags are dropped, then the target; the detail pane has them.

| Key | Action |
|---|---|
| `enter` | connect (§5) |
| `m` | action popup: every action below plus the tools (§8), each with its key |
| `space` / `a` | select / select all, for connecting, checking or running on several hosts |
| `t` | login probe (§4) |
| `n` / `e` / `c` | new host / edit / duplicate (a form: alias, HostName, User, Port, IdentityFile picked from the Keys tab, ProxyJump picked from your hosts, plus "other options" as raw lines) |
| `D` | delete the host's block (type the alias to confirm; backed up, §3) |
| `E` | open the defining file in `$EDITOR` at the block's line |
| `C` | copy a public key to the host (§7, Keys) |
| `x` | run a command on the selected hosts (§10) |
| `i` | host facts: OS, kernel, uptime, load, memory, disk, pending updates, fetched with one `ssh` command and cached |
| `y` | copy: `ssh <alias>`, the hostname, `user@host`, or the resolved config block |
| `w` | open the host's web page (`web` in its metadata, §8) |
| `*` / `h` / `H` | pin / hide / show hidden |
| `/` / `s` / `r` | filter (alias, hostname, user, tags) / sort (frecency, name, status, latency) / re-check now |
| `d`, `J`/`K` | detail pane, scroll it |

Detail pane: effective config with inherited values marked, the file and line that define it, status and latency (or Tailscale's last seen and OS), last probe result, host key type and fingerprint from `known_hosts`, the key it will offer and whether that key is in the agent, last connected and how often, tags and notes, and cached facts.

### Tab 2: Discover

Like folgit's Remote tab: machines you could add but haven't.

- **Tailnet peers** (`tailscale status --json`) not matched by any host.
- **`known_hosts` entries** you've connected to by hand but never gave an alias (hashed entries are skipped).

`space` selects, `enter` adds them as hosts (alias suggested from the machine name, user from your default, both editable before writing), `w` opens Tailscale's admin page for the device.

### Tab 3: Keys

Every key pair in `~/.ssh`: type and size, fingerprint, comment, **⚠ no passphrase**, whether it's loaded in the agent (`ssh-add -l`), and which hosts use it.

| Key | Action |
|---|---|
| `n` | generate a key (`ssh-keygen -t ed25519 -C <user>@<machine>`, in place so it can ask for a passphrase) |
| `A` | add to the agent (in place). On Windows, says so if the `ssh-agent` service is disabled and how to start it |
| `C` | copy the public key to the selected hosts. Built in, because `ssh-copy-id` doesn't exist on Windows: appends to `~/.ssh/authorized_keys` only if it isn't already there, with `umask 077`. A Windows target (detected by the probe's facts) uses `administrators_authorized_keys` for admin users |
| `y` | copy the public key |

### Tab 4: Settings

Toggles and values, saved to `config.toml` as they change (as in folgit, this drops comments in that file; the SSH config is never touched by Settings): SSH config path, connect mode, stay open, check interval and timeout, Tailscale on/off, tools, Powerline status bar.

## 8. Config

`config.toml` in the OS config folder (`%AppData%\qissh`, `~/Library/Application Support/qissh`, `~/.config/qissh`), versioned like folgit's. On first run every tool found on PATH is turned on.

```toml
version = 1
ssh_config = "~/.ssh/config"
alias_name = "qs"
connect_mode = "auto"         # auto | in_place | zellij | wt | shell
stay_open = false
check_interval = "60s"        # "0" turns background checks off
check_timeout = "2s"
tailscale = true
pinned = ["oak", "devbox1"]
hidden = []
powerline = false

# Things ssh config has no place for, keyed by alias. Renaming a host in
# qissh renames its entry here too.
[hosts.oak]
tags = ["home", "pi"]
notes = "RAID 5, 4 × 8 TB; on the UPS"
web = "http://oak.example.ts.net:8123"
mac = "02:00:00:00:00:01"     # for wake-on-LAN (§10)

[[tools]]
name = "VS Code"
cmd = "code"
args = ["--remote", "ssh-remote+{alias}", "{home}"]
key = "o"
mode = "detach"
enabled = true

[[tools]]
name = "SFTP"
cmd = "sftp"
args = ["{alias}"]
key = "f"
mode = "terminal"
enabled = true

[[tools]]
name = "mosh"
cmd = "mosh"
args = ["{alias}"]
key = "M"
mode = "terminal"
enabled = false
```

Tool placeholders: `{alias}`, `{hostname}`, `{user}`, `{port}`, `{home}` (`/home/<user>`, or `/root`).

**Decision: metadata lives in `config.toml`, not in comments in the SSH config.** It keeps `~/.ssh/config` clean and portable, and QiSSH never has to parse its own comments back. The cost is that tags don't travel with the SSH config, which `qs ls --json` / a future export can cover.

State that isn't config (connection history, last status, facts, backups) lives in the OS cache folder.

## 9. Package layout

```
cmd/qissh/      main: flags, subcommands, config, the program
internal/
  config/           TOML config, marks (from folgit)
  history/          connection history (frecency, last host); later also facts and config backups
  sshconfig/        lossless parser, Include, block edits, safe writer
  resolve/          ssh -G, batched and cached
  probe/            TCP reachability, BatchMode login probe and its classification
  tailscale/        status --json, matching peers to hosts
  keys/             key discovery, fingerprints, agent, generate, copy-id
  remote/           run a command on hosts, facts
  run/              Runner interface over ssh, ssh-keygen, ssh-add, tailscale; fake for tests
  launcher/         tools, zellij and Windows Terminal tabs, browser (from folgit)
  shell/            init scripts: qs function and alias completion (from folgit)
  ui/               the Bubble Tea app: one file per tab and view, plus forms, dialogs, help, status bar
```

## 10. More features to consider

Ordered by how much I think they'd be used. The first three are cheap given the design above.

1. **Port forwards / tunnels.** Saved per host (`[[hosts.devbox1.tunnels]] name = "postgres", spec = "L 5432:localhost:5432"`), started with `T` as a background `ssh -N` (`ExitOnForwardFailure=yes`), listed in the status bar while they run and stopped when QiSSH quits (or kept, as a setting). A one-off "forward a port" form for ad-hoc ones.
2. **Run on many hosts (`x`).** Type a command, run it on every selected host in parallel (`BatchMode`), and see the output grouped by host with exit codes, failures first. Saved **snippets** per host or tag (`sudo apt update && sudo apt upgrade -y`, `docker ps`, `df -h`).
3. **Host-key-changed helper** (in §4) and **config doctor** (`!`), like dockgit's: duplicate aliases, hosts shadowed by an earlier wildcard, `IdentityFile`s that don't exist, keys without a passphrase, a world-writable config, a `ProxyJump` to an alias that doesn't exist, new hosts placed after `Host *`. Each finding has a one-line fix.
4. **Wake-on-LAN** (`W`) for a down host with a `mac`, sent from this machine or relayed through another host on the same LAN.
5. **Groups by tag.** Collapsible tag headings (`home`, `dev`, `pi`) instead of a flat list, toggled with `g`.
6. **Jump-host view.** A tree of which hosts go through which `ProxyJump`, with the status of each hop.
7. **File transfer.** `u`/`p` to push or pull a path with `scp`, with tab completion of remote paths over the existing connection; or hand off to a TUI file manager (yazi, termscp) as a tool.
8. **Session logging.** Optional per-host recording of sessions to the cache folder (`script` on Unix, `Start-Transcript`-style on Windows).
9. **Latency history.** A sparkline of the last day's checks in the detail pane, showing a flaky host.
10. **Import.** PuTTY sessions (Windows registry), Termius / Royal TS CSV, or another machine's `~/.ssh/config` into this one, through the same safe writer.
11. **Power actions.** Reboot or shut down selected hosts, with a typed confirmation and a watch that shows them come back up.
12. **ControlMaster helper.** Offer to add `ControlMaster auto` / `ControlPersist` to `Host *` so `t`, `i` and `x` reuse connections and are near-instant (not available on Windows OpenSSH, so hidden there).
13. **Prompt / status integration.** `qs ls --json --down` for a shell prompt or status line segment showing "2 hosts down".

## 11. Milestones

Each ends with something usable, plus tests.

| | Milestone | Done when |
|---|---|---|
| M0 | Skeleton from folgit: tabs, help, Settings, config, status bar | the app starts, switches tabs, themes in light and dark terminals |
| M1 | Read-only Hosts: lossless parser, Include, `ssh -G`, detail pane, connect, history, `qs <query>`, `qs -`, `init` with completion | `qs pi<tab>` completes and connects; every byte of the real config round-trips |
| M2 | Status: TCP checks, Tailscale, login probe, host-key-changed fix | a host turned off shows down within one check interval; a changed host key is fixed from the UI |
| M3 | Editing: new / edit / duplicate / delete, backups, validation, `qs add` | editing a host in a heavily commented test config changes only the expected lines (golden test) |
| M4 | Keys tab, copy-id (Linux and Windows targets), metadata (tags, notes, web) | a fresh key can be generated and pushed to a host, then the probe says ok |
| M5 | Discover tab | a tailnet device not in the config can be added in three key presses |
| M6 | Tunnels, run on many, facts, tools, doctor | — |
| M7 | Polish: every screen fits from 40×10 to 200×40, README and screenshots, CI, GoReleaser releases (folgit's roadmap item), gup entry | — |

## 12. Testing

- **Parser:** round-trip golden files covering real-world mess: mixed keyword case, `key=value`, quoted values with spaces, tabs vs spaces, CRLF, trailing comments, `Host` lines with several patterns, `Include` globs, a file with no trailing newline, a BOM. Edit golden files for every operation.
- **Resolve, probe, keys, tailscale:** recorded output from `ssh -G`, `ssh -v` failures (each probe outcome), `ssh-keygen -l`, `ssh-add -l` and `tailscale status --json` in `testdata/`, through the fake `Runner`.
- **UI:** key presses against a fake `Runner`, checking state and rendered lines; one test renders every screen and dialog at many sizes and checks it fits, as in dockgit.
- **Live (opt-in):** `QS_IT=1` starts an `sshd` container with a known key and password user, and tests connect, probe outcomes (including a swapped host key), copy-id and editing against a temp config via `-F`.

## 13. Risks and open points

- **Lossless writing** is the riskiest part: a bug here damages a file people rely on. Mitigated by byte-identical round-trip tests, validation with `ssh -G` before replacing, backups, and refusing to write over a file that changed underneath.
- **ssh's rules are subtle** (first match wins, `Match` evaluated at connect time, `Include` inside a `Host` block is conditional). Using `ssh -G` for effective values avoids reimplementing them; QiSSH only needs its own parser to *edit*.
- **`Match` blocks** (`Match exec`, `Match host`) can make the effective config depend on the network you're on; `ssh -G` evaluates them for *now*, which is shown as such.
- **Windows OpenSSH** quirks: config ACL checks, no `ControlMaster`, no `ssh-copy-id`, agent as a service. All handled or hidden on Windows, with tests on a Windows CI runner.
- **Background checks** must never hang on a prompt: TCP dials don't authenticate, and anything that runs `ssh` in the background uses `BatchMode=yes` and a `ConnectTimeout`.

## Decisions

Confirmed before building:

1. `enter` connects immediately (it's what you'll do 90% of the time); folgit's action popup moves to `m`.
2. After an in-place session ends, QiSSH quits (`stay_open = false`), since `qs` is mainly a launcher.
3. Tags, notes and other metadata live in `config.toml`, not in SSH config comments.
4. The binary is `qissh` and `qs` comes from the shell integration, so it's renameable and completes aliases.
5. Tailscale is a first-class status source and the Discover tab is in v1 (M5), since most hosts in the current config are on the tailnet.

Made while building:

6. **No startup cache.** Measured: `ssh -G` takes about 35 ms per host and 10 hosts resolve in about 115 ms in parallel. The list draws at once from the file itself (each block's own `HostName` and `User`, dimmed) and fills in ssh's values as they arrive.
7. **In-place sessions run after the TUI exits.** QiSSH quits and `main` runs ssh with the terminal, so the session never shares the screen with a TUI that's about to redraw. Ctrl+C goes to the session. ssh's exit code becomes QiSSH's. Only `stay_open` uses `tea.ExecProcess`.
8. **`qs <query>` passes unknown targets through.** A query that matches no alias but looks like a target (`me@1.2.3.4`, `box.lan`) runs `ssh` with it, so `qs` can replace `ssh` for ad-hoc hosts. It's recorded in the history, so `qs -` repeats it.
9. **Where each value comes from** is worked out by walking the blocks in ssh's reading order with ssh's own pattern rules (case-insensitive `*`/`?`, `!` negation, first match wins). `Match` blocks other than `Match all` are skipped, since only ssh can evaluate them; `ssh -G` still gives the right value, only its source may then read "default".
10. **Rules are rows.** `Host *`, other pattern blocks and `Match` blocks sit under "Defaults & rules" at the bottom of the Hosts list rather than in a separate view; `enter` on one opens it in the editor.
11. **The config is watched** (modification times, every 2 s), so edits made in another pane or with `E` show up without pressing `r`.
12. **`ssh` is whatever is first on PATH**, as in your shell. Under Git Bash on Windows that's Git's OpenSSH; under PowerShell it's Windows' own. QiSSH doesn't pick one.
13. **Editing is pulled forward from M3.** `n` and `e` open one form (alias, HostName, User, Port, IdentityFile, ProxyJump); `e` replaces the planned duplicate-on-`c`, and delete lives in the edit form (with a y/n confirm, since every save is backed up) instead of on `D`. Options the form doesn't manage are left in the block untouched.
14. **New lines follow the file's style.** Keyword case (lowercase or `HostName`) comes from the block being edited, or from the whole file for a new block; indent from the block's first option; line endings from the file.
15. **Tailscale and the TCP check combine.** A tailnet machine that's offline shows Tailscale's "off 3d"; one that's online is still dialled, since being on the tailnet doesn't mean sshd is listening. Peers match by MagicDNS name, short name or Tailscale IP, never by Tailscale's HostName (phones report "localhost").
16. **Login checks run `exit 0`**, which works in sh, cmd and PowerShell, and any exit status other than ssh's own 255 counts as a working login. They use `BatchMode=yes`, `ConnectTimeout=8` and a 20 s hard limit, and run with no terminal (a new session on Unix, no console on Windows), because BatchMode isn't passed on to a ProxyJump's inner ssh.
17. **Host keys are fetched with a throwaway known_hosts** (`StrictHostKeyChecking=accept-new` into a temp file), so the key goes through ProxyJump like a real connection. Trusting appends exactly the lines ssh wrote for the key that was shown, rather than accepting whatever the host presents on the next connect.
18. **`qs check` has `--login`** rather than always logging in, so the default is safe to run from a prompt or cron.
19. **Keys are read, never decoded.** Type, size and fingerprint come from parsing the `.pub` file (the fingerprint matches `ssh-keygen -l`); whether a private key has a passphrase comes from its header alone (the cipher name in an OpenSSH key, `Proc-Type`/`ENCRYPTED` in PEM). No private key material is parsed and nothing is run per key.
20. **ssh-keygen and ssh-add come from beside the ssh in use**, so they match it. On Windows this matters for the agent: Git's ssh-add only knows `SSH_AUTH_SOCK` and can't reach the Windows `ssh-agent` service, so the Keys tab says which situation you're in and how to fix it.
21. **Copying a key runs in the terminal** (like `stay_open` sessions) so ssh can ask for passwords, one host after another under a heading each, and waits for enter only if one failed. The key goes in by its base64 blob, so a copy with another comment counts as already there. Linux and macOS get an `sh -c` script with the key as an argument (safe under bash, zsh and fish, with `umask 077`, a newline fix and `restorecon`); Windows gets PowerShell as `-EncodedCommand`, which needs no quoting under cmd.exe or PowerShell, and picks `administrators_authorized_keys` (with its required ACL) for members of Administrators. Which one a host gets comes from Tailscale's OS, and `w` in the confirmation overrides it. Each host's login is checked afterwards.
22. **`C` from the Keys tab picks hosts on the Hosts tab** (a banner, `space` to select, `C` to copy) rather than in a popup list, so filtering and selection work as usual.
23. **Metadata is edited in the host form**, below the ssh fields, and saved to `config.toml` only; a form that changes only metadata never touches the ssh config. Renaming a host moves its metadata and deleting the host drops it. Tags are lowercase and filter with `/#tag`.
24. **Discover adds through the host form**, opened filled in with Save focused, so a machine is added with `2`, `enter`, `enter` and every value can still be changed first. Several selected machines are added in one save after a confirmation listing what each becomes. Either way you land on the Hosts tab with the new host selected. Added tailnet machines get the tag `tailnet`; the suggested User is the one most of your hosts use (left empty when it's your local user).
25. **What Discover leaves out:** tailnet machines already configured (HostName is a MagicDNS name, short name or Tailscale IP, or the alias is the short name), this machine, hashed and wildcard `known_hosts` entries, git forges, names on the tailnet's domain that are no longer peers (machines removed from the tailnet), and phones and tablets never connected to (shown with `H`). A `known_hosts` entry for a peer's name or IP is folded into that peer, and lines sharing a name are one machine.
26. **`←`/`→` switch tabs** everywhere a text field isn't focused (the forms keep them for the cursor and the key type).
27. **Run on many uses BatchMode with no terminal**, like the login check, so a host that wants a password fails instead of hanging the batch. Commands run on every host at once with a two-minute limit; output is grouped by host, failures first. Snippets are a flat list in `config.toml` (`[[snippets]]`), saved from the prompt with `ctrl+s`, rather than per host or tag.
28. **Facts come from one ssh command**: an `sh -c` script (no single quotes inside, like the key installer) that reads `/proc`, `os-release`, `df` and the package manager's local state (`apt-get -s`, `dnf --cacheonly`, `pacman -Qu`), so it needs no root and no network; Windows hosts (by Tailscale's OS or earlier facts) get PowerShell as `-EncodedCommand`. Facts are cached in `facts.json` with their time and fetched only on `i`, never in the background. Facts also tell the key copier a host runs Windows.
29. **Tunnels are background `ssh -N`** with BatchMode, `ExitOnForwardFailure` and keepalives, started without a terminal. Saved ones live in the host's metadata (`[[hosts.<alias>.tunnels]]`, kept when the host form is saved or renamed). Shorthand like `5432` and `8080:80` expands to `-L` forwards to `localhost` on the host. A tunnel that ends on its own shows ssh's last line as an error; all are stopped when QiSSH exits, including before an in-place session.
30. **The doctor runs on every config or key reload** (it only stats files), and the status bar shows how many warnings and problems there are. An option counts as ignored when ssh's first-match rule takes it from an earlier block (`IdentityFile`, forwards and other options ssh collects from every block are exempt). Permission checks run only off Windows, where ssh enforces them. `qs doctor` prints the same and exits 1 when something needs fixing.
31. **Renamed from quick-ssh to qissh** (qi as in chi). "quick-ssh" was generic and taken (an iOS app, and `quicssh` sounds the same). The binary, module and folders are `qissh`, prose says QiSSH, and `qs` is unchanged. On start the old `quick-ssh` config and cache folders are renamed, unless a `qissh` one already exists.
32. **`qs add` shares the form's code path.** The field checks (`HostFields.Check`), the `ssh -F <file> -G` validator and the main-file lookup moved out of the UI so `qs add` and `n` reject the same things and write the same block in the same place. Flags may come before, between or after the alias and target. A bare IPv6 address takes no port; `[addr]:port` does. There's no `--tag` yet: tags are added in the dashboard.
