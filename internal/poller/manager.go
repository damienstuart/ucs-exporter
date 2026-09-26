// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
)

// ErrLimit is returned when the unlisted-domain limit is reached.
var ErrLimit = errors.New("unlisted domain limit reached")

// BuildModules creates the modules for a domain.
type BuildModules func(config.Resolved) ([]module.Module, error)

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Build     BuildModules
	Logger    *slog.Logger
	UserAgent string
	Transport http.RoundTripper // tests
	// NoJitter disables start-up jitter for static domains (tests).
	NoJitter bool
}

type entry struct {
	p      *Poller
	cancel context.CancelFunc
}

// Manager runs one poller per domain.
type Manager struct {
	ctx context.Context
	o   ManagerOptions
	log *slog.Logger

	mu       sync.Mutex
	cfg      *config.Config
	static   map[string]*entry // key: lower-case name
	unlisted map[string]*entry
	stopped  bool
	wg       sync.WaitGroup
}

// NewManager starts pollers for every configured domain.
func NewManager(ctx context.Context, cfg *config.Config, o ManagerOptions) (*Manager, error) {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	m := &Manager{ctx: ctx, o: o, log: log, static: map[string]*entry{}, unlisted: map[string]*entry{}}
	if err := m.Apply(cfg); err != nil {
		m.Stop()
		return nil, err
	}
	m.wg.Go(m.janitor)
	return m, nil
}

// start starts a poller; if after is non-nil its first poll waits until
// after is closed.
func (m *Manager) start(r config.Resolved, jitter bool, after <-chan struct{}) (*entry, error) {
	mods, err := m.o.Build(r)
	if err != nil {
		return nil, fmt.Errorf("domain %s: %w", r.Name, err)
	}
	p, err := New(Options{Config: r, Modules: mods, Logger: m.log, Jitter: jitter, UserAgent: m.o.UserAgent, Transport: m.o.Transport, After: after})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.wg.Go(func() { p.Run(ctx) })
	return &entry{p: p, cancel: cancel}, nil
}

func stopAndWait(e *entry) {
	e.cancel()
	<-e.p.Done()
}

// Apply reconciles the running pollers with cfg: removed domains are stopped,
// changed domains are restarted, new domains are started. Unlisted pollers
// are stopped and restarted lazily on their next scrape. Apply does not wait
// for old pollers to log out (which can take seconds for an unreachable
// UCSM), so scrapes are not blocked; a replacement poller waits for its
// predecessor before its first poll, so polls of a domain never overlap.
func (m *Manager) Apply(cfg *config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return errors.New("manager stopped")
	}

	wanted := map[string]config.Resolved{}
	for _, r := range cfg.ResolvedDomains() {
		wanted[strings.ToLower(r.Name)] = r
	}
	// Build the new pollers first so that a bad configuration leaves the
	// running ones untouched.
	type change struct {
		key string
		r   config.Resolved
	}
	var starts []change
	for key, r := range wanted {
		if e, ok := m.static[key]; ok && e.p.Config().Equal(r) {
			continue
		}
		starts = append(starts, change{key, r})
	}
	for _, c := range starts {
		if _, err := m.o.Build(c.r); err != nil {
			return fmt.Errorf("domain %s: %w", c.r.Name, err)
		}
		if _, err := c.r.TLSClientConfig(); err != nil {
			return fmt.Errorf("domain %s: %w", c.r.Name, err)
		}
	}

	predecessor := map[string]<-chan struct{}{}
	for key, e := range m.static {
		if r, ok := wanted[key]; !ok || !e.p.Config().Equal(r) {
			e.cancel()
			predecessor[key] = e.p.Done()
			delete(m.static, key)
		}
	}
	for key, e := range m.unlisted {
		e.cancel()
		delete(m.unlisted, key)
	}
	var errs []error
	// Jitter only the initial start; domains restarted by a reload poll
	// immediately so that their scrapes see a short gap.
	jitter := !m.o.NoJitter && m.cfg == nil
	for _, c := range starts {
		e, err := m.start(c.r, jitter, predecessor[c.key])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		m.static[c.key] = e
	}
	m.cfg = cfg
	return errors.Join(errs...)
}

// Get returns the poller for a domain, starting one for an allowed unlisted
// domain. It returns config.ErrNotAllowed or ErrLimit.
func (m *Manager) Get(name string) (*Poller, error) {
	key := strings.ToLower(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil, errors.New("manager stopped")
	}
	if e, ok := m.static[key]; ok {
		return e.p, nil
	}
	if e, ok := m.unlisted[key]; ok {
		e.p.Touch() // under the lock, so the janitor cannot evict it first
		return e.p, nil
	}
	r, err := m.cfg.ResolveUnlisted(key)
	if err != nil {
		return nil, err
	}
	if len(m.unlisted) >= m.cfg.UnlistedDomains.MaxDomains {
		return nil, ErrLimit
	}
	e, err := m.start(r, false, nil)
	if err != nil {
		return nil, err
	}
	m.log.Info("started polling unlisted domain", "domain", r.Name)
	e.p.Touch()
	m.unlisted[key] = e
	return e.p, nil
}

// Counts returns the number of static and unlisted pollers.
func (m *Manager) Counts() (static, unlisted int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.static), len(m.unlisted)
}

// Ready reports whether every static domain has completed its first poll
// (successfully or not).
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.static {
		if e.p.State() == nil {
			return false
		}
	}
	return true
}

// Pollers returns the static pollers followed by the unlisted ones.
func (m *Manager) Pollers() []*Poller {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Poller, 0, len(m.static)+len(m.unlisted))
	for _, name := range sortedKeys(m.static) {
		out = append(out, m.static[name].p)
	}
	for _, name := range sortedKeys(m.unlisted) {
		out = append(out, m.unlisted[name].p)
	}
	return out
}

// Stop stops every poller, waiting for them to log out.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopped = true
	var all []*entry
	for _, e := range m.static {
		all = append(all, e)
	}
	for _, e := range m.unlisted {
		all = append(all, e)
	}
	m.static, m.unlisted = map[string]*entry{}, map[string]*entry{}
	m.mu.Unlock()
	for _, e := range all {
		e.cancel()
	}
	for _, e := range all {
		<-e.p.Done()
	}
}

// Wait blocks until every goroutine started by the manager has exited. The
// manager's context must be cancelled (or Stop called) first.
func (m *Manager) Wait() { m.wg.Wait() }

// janitor stops unlisted pollers that have not been scraped recently.
func (m *Manager) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.evictIdle(time.Now())
		}
	}
}

func (m *Manager) evictIdle(now time.Time) {
	m.mu.Lock()
	if m.cfg == nil {
		m.mu.Unlock()
		return
	}
	idle := time.Duration(m.cfg.UnlistedDomains.IdleTimeout)
	var evict []*entry
	for key, e := range m.unlisted {
		if now.Sub(e.p.LastScrape()) > idle {
			evict = append(evict, e)
			delete(m.unlisted, key)
			m.log.Info("stopped polling idle unlisted domain", "domain", e.p.Name())
		}
	}
	m.mu.Unlock()
	for _, e := range evict {
		stopAndWait(e)
	}
}
