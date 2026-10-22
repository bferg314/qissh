// Package shell produces the wrapper function that `qissh init <shell>`
// prints. It defines qs (or the configured name), completes host aliases,
// and, for connect_mode = "shell", runs the ssh command qissh chose in
// the calling shell: qissh writes it to --cmd-file, one argument per
// line, and the wrapper runs it and adds it to the shell's history.
package shell

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Names lists the supported shells.
func Names() []string {
	names := make([]string, 0, len(scripts))
	for n := range scripts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

var validName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Script returns the wrapper for shell, defining a function called name.
func Script(shell, name string) (string, error) {
	s, ok := scripts[strings.ToLower(shell)]
	if !ok {
		return "", fmt.Errorf("unsupported shell %q (supported: %s)", shell, strings.Join(Names(), ", "))
	}
	if !validName.MatchString(name) {
		return "", fmt.Errorf("alias_name %q isn't a valid function name", name)
	}
	return strings.ReplaceAll(s, "QSNAME", name), nil
}

// Install explains how to hook the wrapper into shell's startup file.
func Install(shell string) string {
	switch strings.ToLower(shell) {
	case "bash":
		return `eval "$(qissh init bash)"    # add to ~/.bashrc`
	case "zsh":
		return `eval "$(qissh init zsh)"     # add to ~/.zshrc`
	case "fish":
		return `qissh init fish | source     # add to ~/.config/fish/config.fish`
	case "powershell", "pwsh":
		return `Invoke-Expression (& qissh init powershell | Out-String)    # add to $PROFILE`
	}
	return ""
}

const bash = `# qissh shell integration: QSNAME opens qissh, QSNAME <host> connects.
QSNAME() {
    local tmp rc
    tmp="$(mktemp -t qissh-cmd.XXXXXX)" || return
    command qissh --cmd-file="$tmp" "$@"
    rc=$?
    if [ -s "$tmp" ]; then
        local args=()
        mapfile -t args < "$tmp"
        rm -f -- "$tmp"
        history -s -- "$(printf '%q ' "${args[@]}")"
        command "${args[@]}"
        return
    fi
    rm -f -- "$tmp"
    return $rc
}
_QSNAME_complete() {
    if [ "$COMP_CWORD" -eq 1 ]; then
        local IFS=$'\n'
        COMPREPLY=($(compgen -W "$(command qissh ls --names 2>/dev/null)" -- "${COMP_WORDS[COMP_CWORD]}"))
    fi
}
complete -F _QSNAME_complete QSNAME
`

const zsh = `# qissh shell integration: QSNAME opens qissh, QSNAME <host> connects.
QSNAME() {
    local tmp rc
    tmp="$(mktemp -t qissh-cmd.XXXXXX)" || return
    command qissh --cmd-file="$tmp" "$@"
    rc=$?
    if [[ -s $tmp ]]; then
        local -a args
        args=("${(@f)$(<$tmp)}")
        rm -f -- "$tmp"
        print -s -- "${(j: :)${(q)args[@]}}"
        command "${args[@]}"
        return
    fi
    rm -f -- "$tmp"
    return $rc
}
_QSNAME() { (( CURRENT == 2 )) && compadd -- ${(f)"$(command qissh ls --names 2>/dev/null)"} }
(( $+functions[compdef] )) && compdef _QSNAME QSNAME
`

const fish = `# qissh shell integration: QSNAME opens qissh, QSNAME <host> connects.
function QSNAME
    set -l tmp (mktemp -t qissh-cmd.XXXXXX); or return
    command qissh --cmd-file=$tmp $argv
    set -l st $status
    set -l args (cat -- $tmp 2>/dev/null)
    rm -f -- $tmp
    if test (count $args) -gt 0
        command $args
        return
    end
    return $st
end
complete -c QSNAME -f -n __fish_is_first_arg -a '(command qissh ls --names 2>/dev/null)'
`

const powershell = `# qissh shell integration: QSNAME opens qissh, QSNAME <host> connects.
function QSNAME {
    param([string]$Query)
    $exe = Get-Command qissh -CommandType Application -ErrorAction Stop | Select-Object -First 1
    $all = @()
    if ($PSBoundParameters.ContainsKey('Query')) { $all += $Query }
    $all += $args
    $tmp = [System.IO.Path]::GetTempFileName()
    try {
        & $exe.Source "--cmd-file=$tmp" @all
        $rc = $LASTEXITCODE
        $cmd = @(Get-Content -LiteralPath $tmp -ErrorAction SilentlyContinue | Where-Object { $_ -ne '' })
    } finally {
        Remove-Item -LiteralPath $tmp -ErrorAction SilentlyContinue
    }
    if ($cmd.Count -gt 0) {
        $line = ($cmd | ForEach-Object { if ($_ -match '\s') { "'" + $_ + "'" } else { $_ } }) -join ' '
        try { [Microsoft.PowerShell.PSConsoleReadLine]::AddToHistory($line) } catch {}
        $rest = @($cmd | Select-Object -Skip 1)
        & $cmd[0] @rest
        return
    }
    $global:LASTEXITCODE = $rc
}
Register-ArgumentCompleter -CommandName QSNAME -ParameterName Query -ScriptBlock {
    param($commandName, $parameterName, $wordToComplete)
    & qissh ls --names 2>$null | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`

var scripts = map[string]string{
	"bash":       bash,
	"zsh":        zsh,
	"fish":       fish,
	"powershell": powershell,
	"pwsh":       powershell,
}
