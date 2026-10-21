package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetaRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := Default()
	c.SetMeta("Oak", HostMeta{Tags: []string{"home", "pi"}, Notes: "UPS on USB0", Web: "http://oak:8123"})
	c.SetMeta("elm", HostMeta{})
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "[hosts.Oak]") || strings.Contains(string(data), "elm") {
		t.Fatalf("saved:\n%s", data)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Meta("oak") // any case
	if strings.Join(m.Tags, ",") != "home,pi" || m.Notes != "UPS on USB0" || m.Web != "http://oak:8123" {
		t.Fatalf("%+v", m)
	}
	c.SetMeta("OAK", HostMeta{Notes: "x"})
	if len(c.Hosts) != 1 || c.Hosts["OAK"] == nil {
		t.Fatalf("setting replaces any case: %v", c.Hosts)
	}
}

func TestParseTags(t *testing.T) {
	if got := strings.Join(ParseTags(" Home, pi #lab,,home "), "|"); got != "home|pi|lab" {
		t.Fatal(got)
	}
}
