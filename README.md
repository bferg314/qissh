# quick-ssh

A terminal dashboard for the hosts in your `~/.ssh/config`. Pick one and you're connected. It also shows what ssh will actually use for each host and where in the file every value comes from.

```
qs                # the dashboard
qs oak            # connect: an exact alias, or the only fuzzy match ("qs gar" → garage-pi)
qs -              # reconnect to the last host
qs ls             # list hosts (--json, --names)
qs check          # which hosts are up (--login also tries logging in)
```

quick-ssh runs your own `ssh`, so the session is exactly what `ssh <alias>` gives you: agents, `ProxyJump`, `ControlMaster` and everything else in your config keep working. Your ssh config stays the source of truth.

## Install

You need Go 1.26 or newer and OpenSSH's `ssh` on your PATH (built into Windows 10 and later, macOS and most Linux distributions).

```sh
go install github.com/bferg314/quick-ssh/cmd/quick-ssh@latest
```

This puts `quick-ssh` in `$(go env GOPATH)/bin` (`~/go/bin`, or `%USERPROFILE%\go\bin` on Windows). Make sure that folder is on your PATH.

### The `qs` command

The shell integration defines `qs` and tab-completes your host aliases. Add the line for your shell to its startup file:

```sh
eval "$(quick-ssh init bash)"                                # ~/.bashrc
eval "$(quick-ssh init zsh)"                                 # ~/.zshrc
quick-ssh init fish | source                                 # ~/.config/fish/config.fish
Invoke-Expression (& quick-ssh init powershell | Out-String)  # $PROFILE
```

If `qs` clashes with something, set `alias_name` in the config and the function gets that name instead.

## The Hosts tab

Every host in your ssh config, including files pulled in with `Include`, with where it connects to (as `ssh -G` resolves it) and when you last connected from quick-ssh. Hosts you use most are listed first.

- `enter` connects. `/` filters by alias or hostname, and `enter` in the filter connects straight away once only one host is left.
- `space` selects several hosts and `enter` opens one tab each (inside zellij or Windows Terminal). `a` selects all and `esc` clears the selection.
- `m` lists everything you can do to the host, with each action's key.
- `y` copies the ssh command, alias, hostname, `user@hostname` or the host's whole block.
- `n` adds a host and `e` edits the selected one: alias, HostName, User, Port, IdentityFile and ProxyJump. The edit form also has **Delete host**, which asks first.
- `E` opens the file that defines the host in your editor, at the host's line. quick-ssh notices when the file changes, whether you edit it from here or anywhere else, and reloads.

### How quick-ssh edits your config

Edits change as little as possible. A changed value rewrites only its own line, in the style it was written: `hostname` stays lowercase, `user=alice` keeps its `=`, and trailing comments stay. A new option goes at the end of the host's block with the block's indent. Other options in the block, comments, blank lines and the rest of the file are untouched. New hosts go before the first `Host *` (or other pattern) or `Match` block, because ssh uses the first value it finds and a host after `Host *` would lose to its defaults. Deleting a host removes its block and the comment lines directly above it. If the block also names other hosts (`Host oak oak2`), only that name is removed.

Every save is checked first. quick-ssh writes the new file next to the old one and asks `ssh -F <new file> -G` to read it. If ssh rejects it, nothing is written and you see ssh's message. It then backs up the old file, to `backups` in quick-ssh's cache folder (the last 10 are kept) and to `config.qs-bak` beside it, swaps the new file in and checks it once more. If the file changed on disk while you were editing, the save is refused rather than overwriting that change.
- `*` pins a host to the top, `h` hides one, `H` shows hidden hosts.
- `s` sorts by most used, name or file order. `r` reloads.
- Tools run with their own key: `o` opens the host in VS Code (Remote - SSH), `f` starts `sftp`, and `M` starts `mosh` when it's installed.

### Is it up?

The **STATUS** column shows each host as `● 12ms` (up, with the time to connect), `○ refused` / `○ timeout` / `○ no dns` (down), or `◌` while it's being checked. quick-ssh checks every host when it starts and every 60 seconds while it's open (change that under **Check every** in Settings). It never logs in to do this: it only opens a TCP connection to the ssh port and closes it again.

- **Tailscale:** if `tailscale` is installed and connected, hosts whose HostName is a tailnet machine (by MagicDNS name, short name or Tailscale IP) use Tailscale's view, so a machine that's off shows `○ off 3d` (last seen 3 days ago) rather than timing out. The detail pane adds its OS.
- **Jump hosts:** a host reached through `ProxyJump` can't be checked directly, so it shows its first jump host's state as `● via elm`.
- **Login check (`t`):** tries to log in to the selected host, or every selected host, with keys only and a short timeout, and runs `exit 0`. It never stops at a password or passphrase prompt. The result goes in the detail pane: login works, needs a password or passphrase, host key not known yet, host key changed, refused, timed out, or doesn't resolve.
- **Host keys:** when the login check finds a host key that changed (`▲ host key!`) or that isn't known yet, it shows the fingerprint `known_hosts` has next to the one the host presents now, with a warning if the key changed. `y` trusts the new key: `ssh-keygen -R` removes the old one (keeping `known_hosts.old`) and exactly the key you were shown is added. To see the key, quick-ssh connects using a throwaway known_hosts file, so nothing is trusted until you say so.

`s` can also sort by status, with hosts that are up first (fastest first). The status bar counts how many hosts are up and down.

From the shell, `qs check` prints the same status for every host (or the ones you name) and exits with status 1 if any are down. `qs check --login` adds the login check.

`Host *`, other pattern blocks and `Match` blocks are listed under **Defaults & rules** at the bottom. `enter` on one opens it in your editor.

### Detail pane

On terminals at least 100 columns wide, a pane beside the list shows the selected host's hostname, user, port, key, jump host and port forwards. Any value that doesn't come from the host's own block says where it does come from, such as `from Host *`, `from line 27` (another block for the same host) or `default` (ssh's built-in value). Below that are your connection history, the file and line that define the host, and the block as written. `d` hides the pane, or on narrower terminals shows it full screen. `J`/`K` scroll it.

Press `?` for every key.

## Where sessions open

The `connect_mode` setting (change it on the Settings tab) decides where a session opens:

| Mode | |
|---|---|
| `auto` (default) | A new zellij tab when quick-ssh runs inside zellij, a new Windows Terminal tab inside Windows Terminal, otherwise this terminal |
| `in_place` | This terminal. quick-ssh quits and ssh takes over, unless **Stay open** is on, in which case you come back to the list when the session ends |
| `zellij` / `wt` | Always a new zellij / Windows Terminal tab |
| `shell` | quick-ssh quits and `qs` runs `ssh <alias>` in your shell, so it lands in your shell history (needs the shell integration) |

`qs <host>` from the command line always connects in the current terminal.

## Config

On first run quick-ssh writes `config.toml` to your OS config folder (`%AppData%\quick-ssh` on Windows, `~/Library/Application Support/quick-ssh` on macOS, `~/.config/quick-ssh` on Linux) and turns on every tool it finds on your PATH.

```toml
ssh_config = "~/.ssh/config"   # another file is passed to ssh with -F
alias_name = "qs"
connect_mode = "auto"           # auto | in_place | zellij | wt | shell
stay_open = false
check_interval = "60s"          # "0" checks only at start and on r
check_timeout = "2s"
tailscale = true
pinned = ["oak"]                # set with * on the Hosts tab
hidden = []                     # set with h

[[tools]]
name = "VS Code"
cmd = "code"
args = ["--remote", "ssh-remote+{alias}", "{home}"]
key = "o"
mode = "detach"                 # starts in the background
enabled = true

[[tools]]
name = "SFTP"
cmd = "sftp"
args = ["{alias}"]
key = "f"
mode = "terminal"               # suspends quick-ssh until the tool exits
enabled = true
```

Tool arguments can use `{alias}`, `{hostname}`, `{user}`, `{port}` and `{home}` (`/home/<user>`, or `/root`). Inside zellij, terminal tools open in a new tab unless they set `in_place = true`.

Connection history is kept in `history.json` in your OS cache folder.

## Build

```
go build ./cmd/quick-ssh
go test ./...
```

## Status

quick-ssh is being built in milestones; see [PLAN.md](PLAN.md). Done so far: reading the config (M1), which hosts are up, the login check and host key fixes (M2), and adding, editing and deleting hosts (the core of M3, pulled forward). Next: the Keys tab (M4). The Discover and Keys tabs are placeholders until M5 and M4.
