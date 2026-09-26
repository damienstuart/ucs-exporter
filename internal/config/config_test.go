// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v2"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var defaultEnv = env(map[string]string{EnvUsername: "envuser", EnvPassword: "envpass"})

func mustParse(t *testing.T, y string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(y), t.TempDir(), defaultEnv)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func TestDefaults(t *testing.T) {
	cfg := mustParse(t, `domains: [{name: ucs1.example.com}]`)
	r, ok := cfg.Lookup("UCS1.example.com")
	if !ok {
		t.Fatal("Lookup failed")
	}
	if r.Address != "https://ucs1.example.com/nuova" || r.Username != "envuser" || string(r.Password) != "envpass" {
		t.Errorf("resolved = %+v", r)
	}
	if r.Interval != 60*time.Second || r.Timeout != 45*time.Second || r.RequestTimeout != 30*time.Second ||
		r.MaxConcurrentRequests != 2 || r.MaxDataAge != 3*time.Minute {
		t.Errorf("timing defaults = %v %v %v %d %v", r.Interval, r.Timeout, r.RequestTimeout, r.MaxConcurrentRequests, r.MaxDataAge)
	}
	if r.Modules != nil || r.Options.VirtualVLANs != "off" || !slices.Equal(r.Options.FirmwareTypes, DefaultFirmwareTypes) {
		t.Errorf("module defaults = %v %+v", r.Modules, r.Options)
	}
	if r.TLS.InsecureSkipVerify {
		t.Error("TLS verification disabled by default")
	}
	if len(cfg.Warnings()) != 0 {
		t.Errorf("warnings = %q", cfg.Warnings())
	}
}

func TestInheritance(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nyc.pw"), []byte("filepass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y := `
defaults:
  username: defuser
  password: defpass
  interval: 2m
  max_data_age: 0s
  tls: {insecure_skip_verify: true}
  modules: [system, faults]
  module_options:
    virtual: {vlans: full}
    hardware: {firmware_types: [system]}
domains:
  - name: lon
  - name: nyc
    address: 10.0.0.5:8443
    username: nycuser
    password_file: nyc.pw
    interval: 30s
    timeout: 20s
    request_timeout: 10s
    max_concurrent_requests: 4
    tls: {server_name: ucs-nyc}
    modules: [capacity]
    module_options:
      virtual: {vlans: "off"}
      ethernet_extended: {iom_host_ports: true}
`
	cfg, err := Parse([]byte(y), dir, defaultEnv)
	if err != nil {
		t.Fatal(err)
	}
	lon, _ := cfg.Lookup("lon")
	if lon.Username != "defuser" || string(lon.Password) != "defpass" || lon.Interval != 2*time.Minute ||
		lon.Timeout != 90*time.Second || lon.RequestTimeout != 30*time.Second || lon.MaxDataAge != 0 {
		t.Errorf("lon = %+v", lon)
	}
	if !lon.TLS.InsecureSkipVerify || !slices.Equal(lon.Modules, []string{"system", "faults"}) ||
		lon.Options.VirtualVLANs != "full" || !slices.Equal(lon.Options.FirmwareTypes, []string{"system"}) {
		t.Errorf("lon inherited = %+v %v %+v", lon.TLS, lon.Modules, lon.Options)
	}

	nyc, _ := cfg.Lookup("nyc")
	if nyc.Address != "https://10.0.0.5:8443/nuova" || nyc.Username != "nycuser" || nyc.Password != "" ||
		nyc.PasswordFile != filepath.Join(dir, "nyc.pw") {
		t.Errorf("nyc creds = %+v", nyc)
	}
	if nyc.TLS.InsecureSkipVerify || nyc.TLS.ServerName != "ucs-nyc" {
		t.Errorf("nyc tls should replace defaults: %+v", nyc.TLS)
	}
	if nyc.Interval != 30*time.Second || nyc.Timeout != 20*time.Second || nyc.RequestTimeout != 10*time.Second ||
		nyc.MaxConcurrentRequests != 4 || nyc.MaxDataAge != 0 {
		t.Errorf("nyc timing = %+v", nyc)
	}
	if !slices.Equal(nyc.Modules, []string{"capacity"}) || nyc.Options.VirtualVLANs != "off" || !nyc.Options.IOMHostPorts ||
		!slices.Equal(nyc.Options.FirmwareTypes, []string{"system"}) {
		t.Errorf("nyc modules = %v %+v", nyc.Modules, nyc.Options)
	}
	user, pass, err := nyc.Credentials().Get()
	if err != nil || user != "nycuser" || pass != "filepass" {
		t.Errorf("credentials = %q %q %v", user, pass, err)
	}
	// The password file is re-read on every call.
	if err := os.WriteFile(filepath.Join(dir, "nyc.pw"), []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, pass, _ := nyc.Credentials().Get(); pass != "rotated" {
		t.Errorf("rotated password = %q", pass)
	}
	if w := strings.Join(cfg.Warnings(), "\n"); !strings.Contains(w, "verification is disabled") {
		t.Errorf("warnings = %q", w)
	}
}

func TestPasswordPrecedence(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "def.pw")
	if err := os.WriteFile(pw, []byte("deffile"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]byte(`
defaults: {password_file: def.pw}
domains:
  - {name: a}
  - {name: b, password: inline}
`), dir, defaultEnv)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.Lookup("a")
	b, _ := cfg.Lookup("b")
	if a.PasswordFile != pw || a.Password != "" {
		t.Errorf("a = %+v", a)
	}
	if b.PasswordFile != "" || b.Password != "inline" {
		t.Errorf("b = %+v", b)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key":          `domains: [{name: a, bogus: 1}]`,
		"no domains":           `defaults: {}`,
		"duplicate":            `domains: [{name: a}, {name: A}]`,
		"missing name":         `domains: [{address: x}]`,
		"both passwords":       `domains: [{name: a, password: x, password_file: /etc/hosts}]`,
		"missing file":         `domains: [{name: a, password_file: /nonexistent/pw}]`,
		"short interval":       `domains: [{name: a, interval: 5s}]`,
		"request > timeout":    `domains: [{name: a, timeout: 10s, request_timeout: 20s}]`,
		"data age < interval":  `domains: [{name: a, max_data_age: 30s}]`,
		"concurrency":          `domains: [{name: a, max_concurrent_requests: 9}]`,
		"vlans":                `domains: [{name: a, module_options: {virtual: {vlans: some}}}]`,
		"scheme":               `domains: [{name: a, address: "ftp://x"}]`,
		"ca file":              `domains: [{name: a, tls: {ca_file: /nonexistent/ca.pem}}]`,
		"proxy":                `domains: [{name: a, proxy_url: "::"}]`,
		"unlisted no allow":    `unlisted_domains: {enabled: true}`,
		"unlisted bad regex":   `unlisted_domains: {enabled: true, allow: ["("]}`,
		"unlisted no password": "defaults: {username: u}\nunlisted_domains: {enabled: true, allow: [x]}",
	}
	for name, y := range cases {
		e := defaultEnv
		if name == "unlisted no password" {
			e = env(nil)
		}
		if _, err := Parse([]byte(y), t.TempDir(), e); err == nil {
			t.Errorf("%s: Parse succeeded", name)
		}
	}
	// No credentials at all.
	if _, err := Parse([]byte(`domains: [{name: a}]`), t.TempDir(), env(nil)); err == nil || !strings.Contains(err.Error(), EnvUsername) {
		t.Errorf("missing credentials err = %v", err)
	}
}

func TestWarnings(t *testing.T) {
	cfg := mustParse(t, `domains: [{name: a, address: "http://a", interval: 20s, timeout: 30s, request_timeout: 10s}]`)
	w := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{"plain http", "shorter than UCSM", "longer than interval"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings missing %q: %s", want, w)
		}
	}
}

func TestUnlisted(t *testing.T) {
	cfg := mustParse(t, `
domains: [{name: static}]
unlisted_domains: {enabled: true, allow: ['ucs-[a-z0-9]+\.example\.com']}
`)
	r, err := cfg.ResolveUnlisted("UCS-lab1.example.com")
	if err != nil || !r.Unlisted || r.Name != "ucs-lab1.example.com" || r.Address != "https://ucs-lab1.example.com/nuova" {
		t.Errorf("ResolveUnlisted = %+v, %v", r, err)
	}
	if cfg.UnlistedDomains.MaxDomains != DefaultUnlistedMaxDomains || time.Duration(cfg.UnlistedDomains.IdleTimeout) != time.Hour {
		t.Errorf("unlisted defaults = %+v", cfg.UnlistedDomains)
	}
	for _, bad := range []string{"evil.example.com", "ucs-lab1.example.com.evil.net", "ucs-x.example.com/../x", "a b", ""} {
		if _, err := cfg.ResolveUnlisted(bad); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("ResolveUnlisted(%q) err = %v", bad, err)
		}
	}
	off := mustParse(t, `domains: [{name: a}]`)
	if _, err := off.ResolveUnlisted("a2"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("disabled unlisted err = %v", err)
	}
}

func TestSecretsRedactedWhenMarshalled(t *testing.T) {
	cfg := mustParse(t, `domains: [{name: a, password: hunter2}]`)
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "hunter2") {
		t.Errorf("marshalled config leaks password:\n%s", out)
	}
}

func TestEqual(t *testing.T) {
	cfg := mustParse(t, `domains: [{name: a}, {name: b, interval: 2m}]`)
	a1, _ := cfg.Lookup("a")
	a2, _ := cfg.Lookup("a")
	b, _ := cfg.Lookup("b")
	if !a1.Equal(a2) || a1.Equal(b) {
		t.Error("Equal mismatch")
	}
}

func TestVLANsOffUnquoted(t *testing.T) {
	// YAML 1.1 reads a bare off as a boolean; it must still work.
	cfg := mustParse(t, "defaults: {module_options: {virtual: {vlans: off}}}\ndomains: [{name: a}, {name: b, module_options: {virtual: {vlans: full}}}]")
	a, _ := cfg.Lookup("a")
	b, _ := cfg.Lookup("b")
	if a.Options.VirtualVLANs != "off" || b.Options.VirtualVLANs != "full" {
		t.Errorf("vlans = %q, %q", a.Options.VirtualVLANs, b.Options.VirtualVLANs)
	}
}

func TestUnlistedAllowCaseInsensitive(t *testing.T) {
	cfg := mustParse(t, "unlisted_domains: {enabled: true, allow: ['UCS-[A-Z0-9]+']}")
	if _, err := cfg.ResolveUnlisted("ucs-lab1"); err != nil {
		t.Errorf("upper-case allow pattern did not match: %v", err)
	}
}
