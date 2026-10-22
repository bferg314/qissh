package config

import (
	"path/filepath"
	"testing"
)

func TestTunnelsAndSnippetsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := Default()
	c.SetMeta("oak", HostMeta{Tunnels: []Tunnel{{Name: "grafana", Spec: "L 3000:localhost:3000"}}})
	if !c.AddSnippet("df -h /") || c.AddSnippet("df -h /") {
		t.Fatal("a snippet is saved once")
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := got.Meta("oak"); len(m.Tunnels) != 1 || m.Tunnels[0].Spec != "L 3000:localhost:3000" || m.Empty() {
		t.Fatalf("%+v", m)
	}
	if len(got.Snippets) != 1 || got.Snippets[0].Cmd != "df -h /" {
		t.Fatalf("%+v", got.Snippets)
	}
}
