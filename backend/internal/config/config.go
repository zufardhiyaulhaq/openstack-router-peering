// Package config loads the router-peering configuration.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// KeystoneConfig is the service account used for Keystone and Neutron calls.
type KeystoneConfig struct {
	AuthURL           string `yaml:"auth_url"`
	Username          string `yaml:"username"`
	Password          string `yaml:"password"`
	ProjectName       string `yaml:"project_name"`
	UserDomainName    string `yaml:"user_domain_name"`
	ProjectDomainName string `yaml:"project_domain_name"`
	Region            string `yaml:"region"`
	Interface         string `yaml:"interface"`
}

// Roles is the Keystone role each action needs.
type Roles struct {
	Request string `yaml:"request"`
	Accept  string `yaml:"accept"`
	Reject  string `yaml:"reject"`
	Repair  string `yaml:"repair"`
	Delete  string `yaml:"delete"`
	View    string `yaml:"view"`
}

// Intervals are the reconciler job periods.
type Intervals struct {
	Work     time.Duration `yaml:"work"`
	Expiry   time.Duration `yaml:"expiry"`
	Drift    time.Duration `yaml:"drift"`
	Sweep    time.Duration `yaml:"sweep"`
	PoolScan time.Duration `yaml:"pool_scan"`
}

// BackoffConfig is the retry backoff for transient errors.
type BackoffConfig struct {
	Initial time.Duration `yaml:"initial"`
	Max     time.Duration `yaml:"max"`
}

// Config is the whole configuration file.
type Config struct {
	Listen                   string         `yaml:"listen"`
	DatabaseURL              string         `yaml:"database_url"`
	Keystone                 KeystoneConfig `yaml:"keystone"`
	CloudAdminProject        string         `yaml:"cloud_admin_project"`
	TransitPool              netip.Prefix   `yaml:"transit_pool"`
	Roles                    Roles          `yaml:"roles"`
	MaxNonTerminalPerProject int            `yaml:"max_non_terminal_per_project"`
	PendingTTL               time.Duration  `yaml:"pending_ttl"`
	Lease                    time.Duration  `yaml:"lease"`
	OrphanGrace              time.Duration  `yaml:"orphan_grace"`
	TokenCacheTTL            time.Duration  `yaml:"token_cache_ttl"`
	Intervals                Intervals      `yaml:"intervals"`
	Backoff                  BackoffConfig  `yaml:"backoff"`
}

// Defaults returns the configuration with every default from the design.
func Defaults() Config {
	return Config{
		Listen: ":9797",
		Keystone: KeystoneConfig{
			Username:          "router-peering",
			ProjectName:       "service",
			UserDomainName:    "Default",
			ProjectDomainName: "Default",
			Region:            "RegionOne",
			Interface:         "internal",
		},
		CloudAdminProject:        "admin",
		TransitPool:              netip.MustParsePrefix("100.64.0.0/16"),
		Roles:                    Roles{Request: "member", Accept: "member", Reject: "member", Repair: "member", Delete: "member", View: "reader"},
		MaxNonTerminalPerProject: 50,
		PendingTTL:               168 * time.Hour,
		Lease:                    2 * time.Minute,
		OrphanGrace:              time.Hour,
		TokenCacheTTL:            5 * time.Minute,
		Intervals:                Intervals{Work: 10 * time.Second, Expiry: time.Minute, Drift: 5 * time.Minute, Sweep: time.Hour, PoolScan: time.Hour},
		Backoff:                  BackoffConfig{Initial: 5 * time.Second, Max: 5 * time.Minute},
	}
}

// Load reads path over Defaults, applies env overrides and validates.
func Load(path string) (Config, error) {
	c := Defaults()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, err
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if v := os.Getenv("RP_DATABASE_URL"); v != "" {
		c.DatabaseURL = v
	}
	if v := os.Getenv("RP_KEYSTONE_PASSWORD"); v != "" {
		c.Keystone.Password = v
	}
	return c, c.Validate()
}

// Validate checks the values the service cannot run without.
func (c Config) Validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("database_url is required"))
	}
	if !c.TransitPool.IsValid() || !c.TransitPool.Addr().Is4() {
		errs = append(errs, errors.New("transit_pool must be an IPv4 prefix"))
	} else if c.TransitPool.Bits() > 29 {
		errs = append(errs, errors.New("transit_pool must be /29 or larger"))
	}
	if c.MaxNonTerminalPerProject < 1 {
		errs = append(errs, errors.New("max_non_terminal_per_project must be at least 1"))
	}
	for name, r := range map[string]string{"request": c.Roles.Request, "accept": c.Roles.Accept, "reject": c.Roles.Reject, "repair": c.Roles.Repair, "delete": c.Roles.Delete, "view": c.Roles.View} {
		if r == "" {
			errs = append(errs, fmt.Errorf("roles.%s must not be empty", name))
		}
	}
	return errors.Join(errs...)
}
