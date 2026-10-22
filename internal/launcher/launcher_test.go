package launcher

import (
	"strings"
	"testing"

	"github.com/bferg314/qissh/internal/config"
)

func TestExpand(t *testing.T) {
	v := Vars{Alias: "oak", HostName: "oak.lan", User: "alice", Port: 2222}
	tool := config.Tool{Cmd: "code", Args: []string{"--remote", "ssh-remote+{alias}", "{home}", "{user}@{hostname}:{port}"}}
	got := strings.Join(Command(tool, v).Args[1:], " ")
	if got != "--remote ssh-remote+oak /home/alice alice@oak.lan:2222" {
		t.Errorf("got %q", got)
	}
	if (Vars{User: "root"}).Home() != "/root" || (Vars{}).Home() != "~" {
		t.Error("home for root and for no user")
	}
}

func TestEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	cases := map[string]string{
		"vim":         "vim +12 /c",
		"code --wait": "code --wait --goto /c:12",
		"hx":          "hx /c:12",
		"someeditor":  "someeditor /c",
	}
	for env, want := range cases {
		t.Setenv("EDITOR", env)
		e := Editor("/c", 12)
		if got := e.Cmd + " " + strings.Join(e.Args, " "); got != want {
			t.Errorf("EDITOR=%s: %q, want %q", env, got, want)
		}
	}
	t.Setenv("EDITOR", "code")
	if Editor("/c", 1).Mode != config.ModeDetach {
		t.Error("code without --wait runs detached")
	}
	t.Setenv("EDITOR", "nvim")
	if Editor("/c", 1).Mode != config.ModeTerminal {
		t.Error("terminal editors take over the terminal")
	}
}

func TestTabs(t *testing.T) {
	z := strings.Join(ZellijTab("oak", []string{"definitely-not-installed", "oak"}).Args, " ")
	if z != "zellij action new-tab --name oak --close-on-exit -- definitely-not-installed oak" {
		t.Errorf("zellij: %q", z)
	}
	w := strings.Join(WTTab("oak", []string{"definitely-not-installed", "oak"}).Args, " ")
	if w != "wt -w 0 new-tab --title oak definitely-not-installed oak" {
		t.Errorf("wt: %q", w)
	}
}
