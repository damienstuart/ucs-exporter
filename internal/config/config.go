// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Package config loads and validates the exporter configuration file.
package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	promcfg "github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
	"go.yaml.in/yaml/v2"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// Environment variables supplying default credentials, as in the original
// Python exporter.
const (
	EnvUsername = "PROM_UCS_USERNAME"
	EnvPassword = "PROM_UCS_PASSWORD"
)

// Defaults applied when a setting is not configured.
const (
	DefaultInterval              = 60 * time.Second
	DefaultMaxConcurrentRequests = 2
	DefaultUnlistedMaxDomains    = 16
	DefaultUnlistedIdleTimeout   = time.Hour
	maxRequestTimeout            = 30 * time.Second
	minInterval                  = 10 * time.Second
)

// DefaultFirmwareTypes are the firmwareRunning types exported by default.
var DefaultFirmwareTypes = []string{
	"system", "switch-kernel", "switch-software", "iocard", "blade-controller",
	"blade-bios", "adaptor", "board-controller", "fex",
}

// Config is the configuration file.
type Config struct {
	Defaults        DomainSettings `yaml:"defaults,omitempty"`
	Domains         []DomainConfig `yaml:"domains,omitempty"`
	UnlistedDomains UnlistedConfig `yaml:"unlisted_domains,omitempty"`

	getenv   func(string) string
	allow    []*regexp.Regexp
	warnings []string
}

// DomainSettings are the settings that can be given in defaults and
// overridden per domain.
type DomainSettings struct {
	Username              string             `yaml:"username,omitempty"`
	Password              promcfg.Secret     `yaml:"password,omitempty"`
	PasswordFile          string             `yaml:"password_file,omitempty"`
	Interval              model.Duration     `yaml:"interval,omitempty"`
	Timeout               model.Duration     `yaml:"timeout,omitempty"`
	RequestTimeout        model.Duration     `yaml:"request_timeout,omitempty"`
	MaxConcurrentRequests int                `yaml:"max_concurrent_requests,omitempty"`
	MaxDataAge            *model.Duration    `yaml:"max_data_age,omitempty"`
	SkipSuspectStats      *bool              `yaml:"skip_suspect_stats,omitempty"`
	TLS                   *promcfg.TLSConfig `yaml:"tls,omitempty"`
	ProxyURL              string             `yaml:"proxy_url,omitempty"`
	Modules               []string           `yaml:"modules,omitempty"`
	ModuleOptions         ModuleOptions      `yaml:"module_options,omitempty"`
}

// DomainConfig is one UCS domain to poll.
type DomainConfig struct {
	// Name is the key used in /metrics?domain=<name> and the value of the
	// domain label. It is matched case-insensitively.
	Name string `yaml:"name"`
	// Address is host[:port] or an https URL; it defaults to Name.
	Address        string `yaml:"address,omitempty"`
	DomainSettings `yaml:",inline"`
}

// UnlistedConfig controls polling of domains requested via ?domain= that are
// not in the configuration file.
type UnlistedConfig struct {
	Enabled bool `yaml:"enabled,omitempty"`
	// Allow is a list of regular expressions (implicitly anchored) that an
	// unlisted domain name must match. Required when enabled, because the
	// exporter sends the default credentials to whatever host is named.
	Allow       []string       `yaml:"allow,omitempty"`
	MaxDomains  int            `yaml:"max_domains,omitempty"`
	IdleTimeout model.Duration `yaml:"idle_timeout,omitempty"`
}

// ModuleOptions are per-module settings. At domain level, each block that is
// present replaces the corresponding defaults block.
type ModuleOptions struct {
	Virtual          *VirtualOptions          `yaml:"virtual,omitempty"`
	EthernetExtended *EthernetExtendedOptions `yaml:"ethernet_extended,omitempty"`
	Hardware         *HardwareOptions         `yaml:"hardware,omitempty"`
}

// VirtualOptions configures the virtual module.
type VirtualOptions struct {
	// VLANs controls VLAN membership metrics: "off" (the default), "count"
	// (per-vNIC counts) or "full" (one series per vNIC and VLAN). Both
	// "count" and "full" query every vnicEtherIf object, which is expensive
	// on large domains (tens of thousands of objects, several seconds).
	VLANs string `yaml:"vlans,omitempty"`
}

// EthernetExtendedOptions configures the ethernet_extended module.
type EthernetExtendedOptions struct {
	// IOMHostPorts adds the packet breakdown, loss and pause counters for
	// IOM/FEX host ports (many series).
	IOMHostPorts bool `yaml:"iom_host_ports,omitempty"`
	// AdaptorUplinkErrors adds adapter-side error counters for the
	// adapter-to-IOM links.
	AdaptorUplinkErrors bool `yaml:"adaptor_uplink_errors,omitempty"`
}

// HardwareOptions configures the hardware module.
type HardwareOptions struct {
	// FirmwareTypes limits firmware info series to these firmwareRunning
	// types. Empty means DefaultFirmwareTypes.
	FirmwareTypes []string `yaml:"firmware_types,omitempty"`
}

// Resolved is the fully merged configuration of one domain.
type Resolved struct {
	Name                  string
	Address               string // endpoint URL
	Username              string
	Password              promcfg.Secret
	PasswordFile          string
	Interval              time.Duration
	Timeout               time.Duration
	RequestTimeout        time.Duration
	MaxConcurrentRequests int
	MaxDataAge            time.Duration // 0 disables carry-forward
	SkipSuspectStats      bool          // leave out statistics objects UCSM flags as suspect
	TLS                   promcfg.TLSConfig
	ProxyURL              string
	Modules               []string // nil means each module's default
	Options               Options
	Unlisted              bool
}

// Options are the module options with defaults applied.
type Options struct {
	VirtualVLANs        string
	IOMHostPorts        bool
	AdaptorUplinkErrors bool
	FirmwareTypes       []string
}

// LoadFile reads and validates a configuration file. Relative paths in the
// file are resolved against the file's directory.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data, filepath.Dir(abs), os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse parses and validates configuration data. dir is used to resolve
// relative paths; getenv supplies the default credential variables.
func Parse(data []byte, dir string, getenv func(string) string) (*Config, error) {
	cfg := &Config{getenv: getenv}
	if err := yaml.UnmarshalStrict(data, cfg); err != nil {
		return nil, err
	}
	cfg.Defaults.setDirectory(dir)
	for i := range cfg.Domains {
		cfg.Domains[i].setDirectory(dir)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *DomainSettings) setDirectory(dir string) {
	if s.PasswordFile != "" && !filepath.IsAbs(s.PasswordFile) {
		s.PasswordFile = filepath.Join(dir, s.PasswordFile)
	}
	if s.TLS != nil {
		s.TLS.SetDirectory(dir)
	}
}

// Warnings returns non-fatal problems found during validation.
func (c *Config) Warnings() []string { return c.warnings }

func (c *Config) warnf(format string, args ...any) {
	c.warnings = append(c.warnings, fmt.Sprintf(format, args...))
}

func (c *Config) validate() error {
	var errs []error
	if err := c.Defaults.validateLevel("defaults"); err != nil {
		errs = append(errs, err)
	}
	seen := map[string]bool{}
	for i, d := range c.Domains {
		where := fmt.Sprintf("domains[%d]", i)
		if d.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", where))
			continue
		}
		where = fmt.Sprintf("domain %q", d.Name)
		key := strings.ToLower(d.Name)
		if seen[key] {
			errs = append(errs, fmt.Errorf("%s: duplicate domain name", where))
		}
		seen[key] = true
		if err := d.validateLevel(where); err != nil {
			errs = append(errs, err)
			continue
		}
		r := c.resolve(d, false)
		if err := c.validateResolved(where, &r); err != nil {
			errs = append(errs, err)
		}
	}

	u := &c.UnlistedDomains
	if u.MaxDomains == 0 {
		u.MaxDomains = DefaultUnlistedMaxDomains
	}
	if u.IdleTimeout == 0 {
		u.IdleTimeout = model.Duration(DefaultUnlistedIdleTimeout)
	}
	for _, p := range u.Allow {
		// Names are lower-cased before matching, so match case-insensitively.
		re, err := regexp.Compile("(?i)^(?:" + p + ")$")
		if err != nil {
			errs = append(errs, fmt.Errorf("unlisted_domains.allow: %w", err))
			continue
		}
		c.allow = append(c.allow, re)
	}
	if u.Enabled {
		if len(u.Allow) == 0 {
			errs = append(errs, errors.New("unlisted_domains.allow is required when unlisted domains are enabled: the exporter sends the default credentials to any host that matches"))
		}
		if u.MaxDomains < 1 {
			errs = append(errs, errors.New("unlisted_domains.max_domains must be at least 1"))
		}
		r := c.resolve(DomainConfig{Name: "unlisted.example", DomainSettings: DomainSettings{}}, true)
		if err := c.validateResolved("unlisted_domains (using defaults)", &r); err != nil {
			errs = append(errs, err)
		}
	}
	if len(c.Domains) == 0 && !u.Enabled {
		errs = append(errs, errors.New("no domains configured"))
	}
	return errors.Join(errs...)
}

// validateLevel checks settings that are invalid regardless of inheritance.
func (s *DomainSettings) validateLevel(where string) error {
	var errs []error
	if s.Password != "" && s.PasswordFile != "" {
		errs = append(errs, fmt.Errorf("%s: password and password_file are mutually exclusive", where))
	}
	if s.MaxConcurrentRequests != 0 && (s.MaxConcurrentRequests < 1 || s.MaxConcurrentRequests > 8) {
		errs = append(errs, fmt.Errorf("%s: max_concurrent_requests must be between 1 and 8", where))
	}
	if s.ProxyURL != "" {
		if u, err := url.Parse(s.ProxyURL); err != nil || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: invalid proxy_url %q", where, s.ProxyURL))
		}
	}
	if v := s.ModuleOptions.Virtual; v != nil && v.VLANs != "" && !slices.Contains([]string{"off", "count", "full"}, v.VLANs) {
		errs = append(errs, fmt.Errorf("%s: module_options.virtual.vlans must be off, count or full", where))
	}
	return errors.Join(errs...)
}

func (c *Config) validateResolved(where string, r *Resolved) error {
	var errs []error
	if r.Username == "" {
		errs = append(errs, fmt.Errorf("%s: no username (set username or %s)", where, EnvUsername))
	}
	if r.PasswordFile != "" {
		if _, err := os.Stat(r.PasswordFile); err != nil {
			errs = append(errs, fmt.Errorf("%s: password_file: %w", where, err))
		}
	} else if r.Password == "" {
		errs = append(errs, fmt.Errorf("%s: no password (set password, password_file or %s)", where, EnvPassword))
	}
	if r.Interval < minInterval {
		errs = append(errs, fmt.Errorf("%s: interval must be at least %s", where, minInterval))
	} else if r.Interval < 30*time.Second {
		c.warnf("%s: interval %s is shorter than UCSM's fastest statistics collection interval (30s)", where, r.Interval)
	}
	if r.Timeout > r.Interval {
		c.warnf("%s: timeout %s is longer than interval %s; polls will overrun", where, r.Timeout, r.Interval)
	}
	if r.RequestTimeout > r.Timeout {
		errs = append(errs, fmt.Errorf("%s: request_timeout %s exceeds timeout %s", where, r.RequestTimeout, r.Timeout))
	}
	if r.MaxDataAge != 0 && r.MaxDataAge < r.Interval {
		errs = append(errs, fmt.Errorf("%s: max_data_age %s is shorter than interval %s (use 0 to disable)", where, r.MaxDataAge, r.Interval))
	}
	if !r.Unlisted {
		if _, err := ucsm.NormalizeURL(r.Address); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		} else if strings.HasPrefix(r.Address, "http://") {
			c.warnf("%s: plain http sends the UCSM password unencrypted", where)
		}
	}
	if err := r.TLS.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("%s: tls: %w", where, err))
	} else if _, err := promcfg.NewTLSConfig(&r.TLS); err != nil {
		errs = append(errs, fmt.Errorf("%s: tls: %w", where, err))
	}
	if r.TLS.InsecureSkipVerify {
		c.warnf("%s: TLS certificate verification is disabled", where)
	}
	return errors.Join(errs...)
}

// resolve merges a domain's settings over the defaults and the environment.
func (c *Config) resolve(d DomainConfig, unlisted bool) Resolved {
	def, s := c.Defaults, d.DomainSettings
	r := Resolved{Name: d.Name, Unlisted: unlisted}

	r.Address = d.Address
	if r.Address == "" {
		r.Address = d.Name
	}
	if u, err := ucsm.NormalizeURL(r.Address); err == nil {
		r.Address = u
	}

	r.Username = firstNonEmpty(s.Username, def.Username, c.env(EnvUsername))
	switch {
	case s.PasswordFile != "":
		r.PasswordFile = s.PasswordFile
	case s.Password != "":
		r.Password = s.Password
	case def.PasswordFile != "":
		r.PasswordFile = def.PasswordFile
	case def.Password != "":
		r.Password = def.Password
	default:
		r.Password = promcfg.Secret(c.env(EnvPassword))
	}

	r.Interval = time.Duration(firstNonZero(s.Interval, def.Interval, model.Duration(DefaultInterval)))
	r.Timeout = time.Duration(firstNonZero(s.Timeout, def.Timeout, model.Duration(r.Interval*3/4)))
	r.RequestTimeout = time.Duration(firstNonZero(s.RequestTimeout, def.RequestTimeout, model.Duration(min(maxRequestTimeout, r.Timeout))))
	r.MaxConcurrentRequests = firstNonZero(s.MaxConcurrentRequests, def.MaxConcurrentRequests, DefaultMaxConcurrentRequests)
	switch {
	case s.MaxDataAge != nil:
		r.MaxDataAge = time.Duration(*s.MaxDataAge)
	case def.MaxDataAge != nil:
		r.MaxDataAge = time.Duration(*def.MaxDataAge)
	default:
		r.MaxDataAge = 3 * r.Interval
	}
	if skip := firstNonNil(s.SkipSuspectStats, def.SkipSuspectStats); skip != nil {
		r.SkipSuspectStats = *skip
	}
	switch {
	case s.TLS != nil:
		r.TLS = *s.TLS
	case def.TLS != nil:
		r.TLS = *def.TLS
	}
	r.ProxyURL = firstNonEmpty(s.ProxyURL, def.ProxyURL)
	switch {
	case s.Modules != nil:
		r.Modules = slices.Clone(s.Modules)
	case def.Modules != nil:
		r.Modules = slices.Clone(def.Modules)
	}

	mo := ModuleOptions{
		Virtual:          firstNonNil(s.ModuleOptions.Virtual, def.ModuleOptions.Virtual),
		EthernetExtended: firstNonNil(s.ModuleOptions.EthernetExtended, def.ModuleOptions.EthernetExtended),
		Hardware:         firstNonNil(s.ModuleOptions.Hardware, def.ModuleOptions.Hardware),
	}
	r.Options = Options{VirtualVLANs: "off", FirmwareTypes: DefaultFirmwareTypes}
	if mo.Virtual != nil && mo.Virtual.VLANs != "" {
		r.Options.VirtualVLANs = mo.Virtual.VLANs
	}
	if mo.EthernetExtended != nil {
		r.Options.IOMHostPorts = mo.EthernetExtended.IOMHostPorts
		r.Options.AdaptorUplinkErrors = mo.EthernetExtended.AdaptorUplinkErrors
	}
	if mo.Hardware != nil && len(mo.Hardware.FirmwareTypes) > 0 {
		r.Options.FirmwareTypes = slices.Clone(mo.Hardware.FirmwareTypes)
	}
	return r
}

func (c *Config) env(name string) string {
	if c.getenv == nil {
		return ""
	}
	return c.getenv(name)
}

// Domains returns the resolved configuration of every configured domain.
func (c *Config) ResolvedDomains() []Resolved {
	out := make([]Resolved, 0, len(c.Domains))
	for _, d := range c.Domains {
		out = append(out, c.resolve(d, false))
	}
	return out
}

// Lookup returns the configured domain with the given name
// (case-insensitive).
func (c *Config) Lookup(name string) (Resolved, bool) {
	for _, d := range c.Domains {
		if strings.EqualFold(d.Name, name) {
			return c.resolve(d, false), true
		}
	}
	return Resolved{}, false
}

// ErrNotAllowed is returned for unlisted domains that are disabled or do not
// match the allowlist.
var ErrNotAllowed = errors.New("domain is not configured")

// ResolveUnlisted returns the configuration for an unlisted domain, using the
// defaults, if unlisted domains are enabled and name matches the allowlist.
func (c *Config) ResolveUnlisted(name string) (Resolved, error) {
	if !c.UnlistedDomains.Enabled {
		return Resolved{}, ErrNotAllowed
	}
	name = strings.ToLower(name)
	if !validHostname(name) {
		return Resolved{}, ErrNotAllowed
	}
	for _, re := range c.allow {
		if re.MatchString(name) {
			r := c.resolve(DomainConfig{Name: name}, true)
			return r, nil
		}
	}
	return Resolved{}, ErrNotAllowed
}

var hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

func validHostname(s string) bool { return len(s) <= 253 && hostnameRE.MatchString(s) }

// Credentials returns the credentials for the domain. A password file is
// re-read on every call so it can be rotated without a reload.
func (r Resolved) Credentials() ucsm.Credentials {
	return credentials{user: r.Username, password: string(r.Password), file: r.PasswordFile}
}

type credentials struct{ user, password, file string }

func (c credentials) Get() (string, string, error) {
	if c.file == "" {
		return c.user, c.password, nil
	}
	b, err := os.ReadFile(c.file)
	if err != nil {
		return "", "", err
	}
	return c.user, strings.TrimRight(string(b), "\r\n"), nil
}

// TLSClientConfig builds the tls.Config for the domain.
func (r Resolved) TLSClientConfig() (*tls.Config, error) {
	return promcfg.NewTLSConfig(&r.TLS)
}

// Equal reports whether two resolved configurations are identical.
func (r Resolved) Equal(o Resolved) bool {
	return fmt.Sprintf("%#v", r) == fmt.Sprintf("%#v", o)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func firstNonZero[T comparable](v ...T) T {
	var zero T
	for _, x := range v {
		if x != zero {
			return x
		}
	}
	return zero
}

func firstNonNil[T any](v ...*T) *T {
	for _, x := range v {
		if x != nil {
			return x
		}
	}
	return nil
}
