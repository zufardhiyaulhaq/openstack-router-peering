package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.TransitPool != netip.MustParsePrefix("100.64.0.0/16") {
		t.Errorf("TransitPool = %v", c.TransitPool)
	}
	if c.Listen != ":9797" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.MaxNonTerminalPerProject != 50 {
		t.Errorf("MaxNonTerminalPerProject = %d", c.MaxNonTerminalPerProject)
	}
	if c.PendingTTL != 168*time.Hour {
		t.Errorf("PendingTTL = %v", c.PendingTTL)
	}
	if c.Roles.Accept != "member" || c.Roles.View != "reader" {
		t.Errorf("Roles = %+v", c.Roles)
	}
	if c.Backoff.Max != 5*time.Minute || c.Backoff.Initial != 5*time.Second {
		t.Errorf("Backoff = %+v", c.Backoff)
	}
	if c.Lease != 2*time.Minute || c.TokenCacheTTL != 5*time.Minute || c.OrphanGrace != time.Hour {
		t.Errorf("durations = %v %v %v", c.Lease, c.TokenCacheTTL, c.OrphanGrace)
	}
	want := Intervals{Work: 10 * time.Second, Expiry: time.Minute, Drift: 5 * time.Minute, Sweep: time.Hour, PoolScan: time.Hour}
	if c.Intervals != want {
		t.Errorf("Intervals = %+v", c.Intervals)
	}
	if c.CloudAdminProject != "admin" {
		t.Errorf("CloudAdminProject = %q", c.CloudAdminProject)
	}
}

func TestLoadOverlayAndEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("transit_pool: 10.250.0.0/20\ndatabase_url: x\nintervals:\n  work: 3s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RP_DATABASE_URL", "y")
	t.Setenv("RP_KEYSTONE_PASSWORD", "s")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.TransitPool != netip.MustParsePrefix("10.250.0.0/20") {
		t.Errorf("TransitPool = %v", c.TransitPool)
	}
	if c.DatabaseURL != "y" || c.Keystone.Password != "s" {
		t.Errorf("env overrides not applied: %q %q", c.DatabaseURL, c.Keystone.Password)
	}
	if c.Intervals.Work != 3*time.Second || c.Intervals.Drift != 5*time.Minute {
		t.Errorf("Intervals = %+v", c.Intervals)
	}
}

func TestValidateRejects(t *testing.T) {
	base := Defaults()
	base.DatabaseURL = "postgres://x"
	if err := base.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]func(*Config){
		"pool /30":   func(c *Config) { c.TransitPool = netip.MustParsePrefix("10.0.0.0/30") },
		"pool ipv6":  func(c *Config) { c.TransitPool = netip.MustParsePrefix("fd00::/64") },
		"no db url":  func(c *Config) { c.DatabaseURL = "" },
		"zero limit": func(c *Config) { c.MaxNonTerminalPerProject = 0 },
		"empty role": func(c *Config) { c.Roles.Accept = "" },
	}
	for name, mut := range cases {
		c := base
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
