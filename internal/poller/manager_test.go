// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package poller

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
)

func parseCfg(t *testing.T, y string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(y), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const managerCfg = `
defaults: {username: mon, password: pw}
domains: [{name: ucs1}, {name: UCS2}]
unlisted_domains: {enabled: true, allow: ['lab-[0-9]+'], max_domains: 1, idle_timeout: 10m}
`

func build(config.Resolved) ([]module.Module, error) { return []module.Module{bladeModule{}}, nil }

func TestManager(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := fake(t)
		ctx, cancel := context.WithCancel(context.Background())
		m, err := NewManager(ctx, parseCfg(t, managerCfg), ManagerOptions{Build: build, Transport: s.RoundTripper()})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { cancel(); m.Stop(); m.Wait() }()

		if m.Ready() {
			t.Error("ready before first polls")
		}
		time.Sleep(20 * time.Second) // past the start-up jitter
		synctest.Wait()
		if !m.Ready() {
			t.Error("not ready after first polls")
		}

		p1, err := m.Get("UCS1")
		if err != nil || p1.Name() != "ucs1" {
			t.Fatalf("Get(UCS1) = %v, %v", p1, err)
		}
		if p2, err := m.Get("ucs2"); err != nil || p2.Name() != "UCS2" {
			t.Fatalf("Get(ucs2) = %v, %v", p2, err)
		}
		if _, err := m.Get("evil.example.com"); !errors.Is(err, config.ErrNotAllowed) {
			t.Errorf("Get(evil) err = %v", err)
		}
		lab, err := m.Get("lab-1")
		if err != nil || lab.Config().Unlisted != true {
			t.Fatalf("Get(lab-1) = %v, %v", lab, err)
		}
		if again, _ := m.Get("LAB-1"); again != lab {
			t.Error("unlisted poller not reused")
		}
		if _, err := m.Get("lab-2"); !errors.Is(err, ErrLimit) {
			t.Errorf("Get(lab-2) err = %v, want limit", err)
		}
		if st, ul := m.Counts(); st != 2 || ul != 1 {
			t.Errorf("counts = %d, %d", st, ul)
		}

		// Idle unlisted pollers are evicted.
		time.Sleep(12 * time.Minute)
		synctest.Wait()
		if _, ul := m.Counts(); ul != 0 {
			t.Errorf("unlisted after idle = %d", ul)
		}

		// Reload: ucs1 unchanged, UCS2 changed, ucs3 added.
		if err := m.Apply(parseCfg(t, `
defaults: {username: mon, password: pw}
domains: [{name: ucs1}, {name: UCS2, interval: 2m}, {name: ucs3}]
`)); err != nil {
			t.Fatal(err)
		}
		if p, _ := m.Get("ucs1"); p != p1 {
			t.Error("unchanged domain was restarted")
		}
		if p, _ := m.Get("ucs2"); p.Config().Interval != 2*time.Minute {
			t.Error("changed domain not restarted")
		}
		if _, err := m.Get("ucs3"); err != nil {
			t.Errorf("added domain: %v", err)
		}
		if _, err := m.Get("lab-1"); !errors.Is(err, config.ErrNotAllowed) {
			t.Errorf("unlisted after reload err = %v", err)
		}
		if st, _ := m.Counts(); st != 3 {
			t.Errorf("static after reload = %d", st)
		}

		// Remove a domain.
		if err := m.Apply(parseCfg(t, `
defaults: {username: mon, password: pw}
domains: [{name: ucs1}]
`)); err != nil {
			t.Fatal(err)
		}
		if st, _ := m.Counts(); st != 1 {
			t.Errorf("static after removal = %d", st)
		}
	})
}

func TestManagerStopLogsOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := fake(t)
		ctx, cancel := context.WithCancel(context.Background())
		m, err := NewManager(ctx, parseCfg(t, managerCfg), ManagerOptions{Build: build, Transport: s.RoundTripper(), NoJitter: true})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if s.Sessions() != 2 {
			t.Errorf("sessions = %d, want 2", s.Sessions())
		}
		m.Stop()
		cancel()
		m.Wait()
		if s.Sessions() != 0 {
			t.Errorf("sessions after stop = %d", s.Sessions())
		}
	})
}

// A reload must not block scrapes while old pollers log out, and the
// replacement poller must not poll before its predecessor has stopped.
func TestApplyDoesNotBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := fake(t)
		ctx, cancel := context.WithCancel(context.Background())
		m, err := NewManager(ctx, parseCfg(t, "defaults: {username: mon, password: pw}\ndomains: [{name: a}, {name: b}]"),
			ManagerOptions{Build: build, Transport: s.RoundTripper(), NoJitter: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { cancel(); m.Stop(); m.Wait() }()
		time.Sleep(time.Second)
		synctest.Wait()
		s.DelayMethod("aaaLogout", 4*time.Second)

		start := time.Now()
		if err := m.Apply(parseCfg(t, "defaults: {username: mon, password: pw}\ndomains: [{name: a, interval: 2m}, {name: b}]")); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Get("b"); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > 0 {
			t.Errorf("Apply and Get blocked for %v", d)
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		var logoutEnd, login2 time.Time
		logins := 0
		for _, r := range s.Requests() {
			switch r.Method {
			case "aaaLogout":
				logoutEnd = r.End
			case "aaaLogin":
				logins++
				if logins == 3 {
					login2 = r.Start
				}
			}
		}
		if logins != 3 || login2.Before(logoutEnd) {
			t.Errorf("logins=%d; new poller logged in at %v before the old one logged out at %v", logins, login2, logoutEnd)
		}
	})
}
