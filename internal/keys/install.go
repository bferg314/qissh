package keys

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// unixScript appends the key ($1) to ~/.ssh/authorized_keys unless a line
// already holds its blob ($2), creating the directory and file private to
// the user. It runs under sh whatever the login shell is, and has no
// single quotes or backslashes so it survives being quoted for any shell.
const unixScript = `umask 077; d="$HOME/.ssh"; f="$d/authorized_keys"; ` +
	`mkdir -p "$d" && touch "$f" || exit 1; ` +
	`if grep -qF "$2" "$f"; then echo "quick-ssh: the key is already in $f on $(uname -n)"; exit 0; fi; ` +
	`if [ -s "$f" ] && [ -n "$(tail -c1 "$f")" ]; then echo >> "$f"; fi; ` +
	`echo "$1" >> "$f" || exit 1; ` +
	`command -v restorecon >/dev/null 2>&1 && restorecon -F "$d" "$f" >/dev/null 2>&1; ` +
	`echo "quick-ssh: added the key to $f on $(uname -n)"`

// UnixCommand is the remote command that installs public key line pub on
// a Linux, macOS or BSD host.
func UnixCommand(pub string) string {
	return "sh -c " + shQuote(unixScript) + " quick-ssh " + shQuote(pub) + " " + shQuote(blobOf(pub))
}

// WindowsScript is the PowerShell that installs pub on a Windows host
// running OpenSSH Server. Members of Administrators log in with
// %ProgramData%\ssh\administrators_authorized_keys (sshd_config's default
// Match Group administrators), which must be writable only by
// Administrators and SYSTEM; everyone else uses ~\.ssh\authorized_keys.
// file, when set, is used instead of either (for tests).
func WindowsScript(pub, file string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("$k = " + psQuote(pub) + "\n")
	b.WriteString("$b = " + psQuote(blobOf(pub)) + "\n")
	if file != "" {
		b.WriteString("$admin = $false\n$f = " + psQuote(file) + "\n")
	} else {
		b.WriteString(`$admin = [Security.Principal.WindowsIdentity]::GetCurrent().Groups | Where-Object { $_.Value -eq 'S-1-5-32-544' }
if ($admin) {
  $f = Join-Path $env:ProgramData 'ssh\administrators_authorized_keys'
} else {
  $d = Join-Path $HOME '.ssh'
  New-Item -ItemType Directory -Force -Path $d | Out-Null
  $f = Join-Path $d 'authorized_keys'
}
`)
	}
	b.WriteString(`if ((Test-Path $f) -and (Select-String -Path $f -SimpleMatch -Quiet -Pattern $b)) {
  "quick-ssh: the key is already in $f on $env:COMPUTERNAME"
  exit 0
}
$nl = ''
if ((Test-Path $f) -and (Get-Item $f).Length -gt 0) {
  $bytes = [IO.File]::ReadAllBytes($f)
  if ($bytes[-1] -ne 10) { $nl = "` + "`r`n" + `" }
}
[IO.File]::AppendAllText($f, $nl + $k + "` + "`r`n" + `", (New-Object Text.UTF8Encoding $false))
if ($admin) { icacls.exe $f /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F' | Out-Null }
"quick-ssh: added the key to $f on $env:COMPUTERNAME"
`)
	return b.String()
}

// WindowsCommand is the remote command that installs pub on a Windows
// host. -EncodedCommand needs no quoting, so it works whether the
// server's shell is cmd.exe or PowerShell.
func WindowsCommand(pub string) string {
	return "powershell -NoProfile -NonInteractive -EncodedCommand " + EncodePowerShell(WindowsScript(pub, ""))
}

// EncodePowerShell is script as -EncodedCommand wants it: base64 of
// UTF-16LE.
func EncodePowerShell(script string) string {
	u := utf16.Encode([]rune(script))
	buf := make([]byte, 2*len(u))
	for i, c := range u {
		buf[2*i], buf[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func blobOf(pub string) string {
	if f := strings.Fields(pub); len(f) >= 2 {
		return f[1]
	}
	return pub
}

// shQuote single-quotes s for a POSIX shell (and bash, zsh, fish).
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// GenerateArgs are ssh-keygen's arguments for a new key of type typ
// (ed25519, rsa or ecdsa) at path. ssh-keygen asks for the passphrase
// itself.
func GenerateArgs(typ, path, comment string) []string {
	args := []string{"-t", typ}
	if typ == "rsa" {
		args = append(args, "-b", "4096")
	}
	return append(args, "-C", comment, "-f", path)
}

// FreeName is a key file name in dir that isn't taken: id_<typ>, else
// id_<typ>_<suffix>, else that with _2, _3...
func FreeName(dir, typ, suffix string) string {
	taken := func(n string) bool {
		_, err1 := os.Stat(filepath.Join(dir, n))
		_, err2 := os.Stat(filepath.Join(dir, n+".pub"))
		return err1 == nil || err2 == nil
	}
	base := "id_" + typ
	if !taken(base) {
		return base
	}
	if suffix != "" {
		base += "_" + suffix
		if !taken(base) {
			return base
		}
	}
	for i := 2; ; i++ {
		if name := base + "_" + strconv.Itoa(i); !taken(name) {
			return name
		}
	}
}
