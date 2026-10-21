package shell

import (
	"strings"
	"testing"
)

func TestScript(t *testing.T) {
	for _, sh := range Names() {
		s, err := Script(sh, "qs")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(s, "QSNAME") || !strings.Contains(s, "quick-ssh ls --names") || !strings.Contains(s, "--cmd-file") {
			t.Errorf("%s script:\n%s", sh, s)
		}
		if Install(sh) == "" {
			t.Errorf("%s has no install line", sh)
		}
	}
	if s, _ := Script("bash", "ssq"); !strings.Contains(s, "ssq() {") || !strings.Contains(s, "complete -F _ssq_complete ssq") {
		t.Errorf("the name is configurable:\n%s", s)
	}
	if _, err := Script("bash", "q s; rm -rf /"); err == nil {
		t.Error("names that aren't identifiers are refused")
	}
	if _, err := Script("tcsh", "qs"); err == nil {
		t.Error("unknown shells are refused")
	}
}
