// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Package poller polls UCS domains in the background and keeps the latest
// rendered metrics for each.
package poller

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// Poll results.
const (
	ResultSuccess = "success" // logged in and every class retrieved
	ResultPartial = "partial" // logged in and at least one class retrieved
	ResultFailure = "failure"
)

// ClassStatus is the outcome of querying one class in the latest poll.
type ClassStatus struct {
	Duration    time.Duration
	Objects     int
	Err         error
	Stale       bool      // data carried forward from an earlier poll
	LastSuccess time.Time // zero if never retrieved
}

// State is the immutable result of a poll.
type State struct {
	Families    []*dto.MetricFamily
	PollStart   time.Time
	PollEnd     time.Time
	LastSuccess time.Time // end of the latest success or partial poll
	Result      string
	Err         error // login error, if the poll could not log in
	RenderErr   error // metrics rejected while rendering
	Classes     map[string]ClassStatus
	Modules     map[string]module.ModuleStatus
	Session     ucsm.SessionInfo
}

// Up reports whether the poll succeeded at least partially.
func (s *State) Up() bool { return s.Result == ResultSuccess || s.Result == ResultPartial }

// Options configures a Poller.
type Options struct {
	Config  config.Resolved
	Modules []module.Module
	Logger  *slog.Logger
	// Jitter delays the first poll by a random fraction of the interval (up
	// to 15s) so that many domains do not poll in lock step.
	Jitter    bool
	UserAgent string
	// Transport overrides the HTTP transport (tests).
	Transport http.RoundTripper
	// After, if set, delays the first poll until it is closed. A poller
	// replacing another one (configuration reload) waits for the old one to
	// stop, so that polls of a domain never overlap.
	After <-chan struct{}
}

// Poller polls one domain.
type Poller struct {
	cfg     config.Resolved
	mods    []module.Module
	queries []module.Query
	log     *slog.Logger
	jitter  bool
	after   <-chan struct{}
	sess    *ucsm.Session

	state      atomic.Pointer[State]
	firstDone  chan struct{}
	firstOnce  sync.Once
	lastScrape atomic.Int64
	done       chan struct{}

	mu          sync.Mutex // guards the counters below
	polls       map[string]float64
	overruns    float64
	classErrors map[string]float64
	sessionOps  map[[2]string]float64

	// Owned by the Run goroutine.
	prev     map[string]*module.ClassData
	lastOK   map[string]time.Time // last successful query per class
	lastDur  map[string]time.Duration
	lastErrs map[string]string
	lastRes  string
}

// New creates a poller. Call Run to start polling.
func New(o Options) (*Poller, error) {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log = log.With("domain", o.Config.Name)
	tlsCfg, err := o.Config.TLSClientConfig()
	if err != nil {
		return nil, fmt.Errorf("domain %s: %w", o.Config.Name, err)
	}
	var proxy *url.URL
	if o.Config.ProxyURL != "" {
		if proxy, err = url.Parse(o.Config.ProxyURL); err != nil {
			return nil, fmt.Errorf("domain %s: proxy_url: %w", o.Config.Name, err)
		}
	}
	client, err := ucsm.NewClient(ucsm.ClientConfig{
		Address:   o.Config.Address,
		TLS:       tlsCfg,
		ProxyURL:  proxy,
		MaxConns:  o.Config.MaxConcurrentRequests + 1,
		UserAgent: o.UserAgent,
		Transport: o.Transport,
	})
	if err != nil {
		return nil, fmt.Errorf("domain %s: %w", o.Config.Name, err)
	}
	p := &Poller{
		cfg:         o.Config,
		mods:        o.Modules,
		queries:     module.MergeQueries(o.Modules),
		log:         log,
		jitter:      o.Jitter,
		after:       o.After,
		firstDone:   make(chan struct{}),
		done:        make(chan struct{}),
		polls:       map[string]float64{},
		classErrors: map[string]float64{},
		sessionOps:  map[[2]string]float64{},
		prev:        map[string]*module.ClassData{},
		lastOK:      map[string]time.Time{},
		lastDur:     map[string]time.Duration{},
		lastErrs:    map[string]string{},
	}
	p.sess = ucsm.NewSession(client, o.Config.Credentials(), ucsm.SessionOptions{
		OnEvent: p.sessionEvent,
		Logger:  log,
	})
	return p, nil
}

// Name returns the domain name.
func (p *Poller) Name() string { return p.cfg.Name }

// Config returns the domain configuration.
func (p *Poller) Config() config.Resolved { return p.cfg }

// Queries returns the merged queries of the poller's modules.
func (p *Poller) Queries() []module.Query { return p.queries }

// State returns the latest poll result, or nil before the first poll.
func (p *Poller) State() *State { return p.state.Load() }

// WaitFirst blocks until the first poll has completed or ctx is done.
func (p *Poller) WaitFirst(ctx context.Context) error {
	select {
	case <-p.firstDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done is closed when Run has returned.
func (p *Poller) Done() <-chan struct{} { return p.done }

// Touch records a scrape (used to evict idle unlisted domains).
func (p *Poller) Touch() { p.lastScrape.Store(time.Now().UnixNano()) }

// LastScrape returns the time of the latest scrape.
func (p *Poller) LastScrape() time.Time { return time.Unix(0, p.lastScrape.Load()) }

func (p *Poller) sessionEvent(op, result string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessionOps[[2]string{op, result}]++
}

// Run polls until ctx is cancelled, then logs out.
func (p *Poller) Run(ctx context.Context) {
	defer close(p.done)
	defer p.shutdown()
	if p.after != nil {
		select {
		case <-p.after:
		case <-ctx.Done():
			return
		}
	}

	var delay time.Duration
	if p.jitter {
		limit := min(p.cfg.Interval/4, 15*time.Second)
		if limit > 0 {
			delay = rand.N(limit)
		}
	}
	next := time.Now().Add(delay)
	pollTimer := time.NewTimer(delay)
	defer pollTimer.Stop()
	for {
		// Refresh the session between polls if it would otherwise expire
		// (intervals longer than half the refresh period).
		var (
			refreshTimer *time.Timer
			refresh      <-chan time.Time
		)
		if due := p.sess.RefreshDue(); !due.IsZero() && due.After(time.Now()) && due.Before(next) {
			refreshTimer = time.NewTimer(time.Until(due))
			refresh = refreshTimer.C
		}
		select {
		case <-ctx.Done():
			return
		case <-refresh:
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := p.sess.Refresh(rctx); err != nil {
				p.log.Debug("session refresh failed", "err", err)
			}
			cancel()
		case <-pollTimer.C:
			start := time.Now()
			p.pollOnce(ctx)
			next = start.Add(p.cfg.Interval)
			if now := time.Now(); now.After(next) {
				p.mu.Lock()
				p.overruns++
				p.mu.Unlock()
				p.log.Warn("poll took longer than the interval", "duration", now.Sub(start), "interval", p.cfg.Interval)
				next = now.Add(min(p.cfg.Interval/10, 5*time.Second))
			}
			pollTimer.Reset(time.Until(next))
		}
		if refreshTimer != nil {
			refreshTimer.Stop()
		}
	}
}

func (p *Poller) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.sess.Logout(ctx); err != nil {
		p.log.Debug("logout failed", "err", err)
	}
	p.sess.Client().CloseIdleConnections()
}

type fetchResult struct {
	objs []*ucsm.MO
	err  error
	dur  time.Duration
}

// PollOnce performs a single poll immediately. It must not be called
// concurrently with Run or itself; it is meant for tests and one-shot use.
func (p *Poller) PollOnce(ctx context.Context) *State {
	p.pollOnce(ctx)
	return p.State()
}

func (p *Poller) pollOnce(ctx context.Context) {
	start := time.Now()
	pctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	var results map[string]fetchResult
	loginErr := p.sess.Ensure(pctx)
	if loginErr == nil {
		results = p.fetchAll(pctx)
	}
	if ctx.Err() != nil {
		// Shutting down: do not publish a poll that was cut short.
		return
	}
	now := time.Now()

	classes := make(map[string]*module.ClassData, len(p.queries))
	status := make(map[string]ClassStatus, len(p.queries))
	ok := 0
	for _, q := range p.queries {
		res, fetched := results[q.Class]
		if !fetched {
			res.err = loginErr
		}
		st := ClassStatus{Duration: res.dur, Err: res.err}
		if res.err == nil {
			ok++
			cd := &module.ClassData{Objects: res.objs, FetchedAt: now}
			classes[q.Class], p.prev[q.Class] = cd, cd
			st.Objects, st.LastSuccess = len(res.objs), now
			p.lastOK[q.Class] = now
			p.lastDur[q.Class] = res.dur
		} else {
			p.mu.Lock()
			p.classErrors[q.Class]++
			p.mu.Unlock()
			st.LastSuccess = p.lastOK[q.Class]
			if prev := p.prev[q.Class]; prev != nil {
				if p.cfg.MaxDataAge > 0 && now.Sub(prev.FetchedAt) <= p.cfg.MaxDataAge {
					classes[q.Class] = &module.ClassData{Objects: prev.Objects, FetchedAt: prev.FetchedAt, Stale: true}
					st.Stale, st.Objects = true, len(prev.Objects)
				} else {
					delete(p.prev, q.Class)
				}
			}
		}
		status[q.Class] = st
	}

	result := ResultFailure
	switch {
	case loginErr != nil:
	case ok == len(p.queries):
		result = ResultSuccess
	case ok > 0:
		result = ResultPartial
	}

	snap := module.NewSnapshot(p.cfg.Name, classes)
	fams, mods, renderErr := module.Render(snap, p.mods, prometheus.Labels{"domain": p.cfg.Name})
	end := time.Now()

	p.mu.Lock()
	p.polls[result]++
	p.mu.Unlock()

	st := &State{
		Families:  fams,
		PollStart: start,
		PollEnd:   end,
		Result:    result,
		Err:       loginErr,
		RenderErr: renderErr,
		Classes:   status,
		Modules:   mods,
		Session:   p.sess.Info(),
	}
	if prev := p.State(); prev != nil {
		st.LastSuccess = prev.LastSuccess
	}
	if st.Up() {
		st.LastSuccess = end
	}
	p.state.Store(st)
	p.firstOnce.Do(func() { close(p.firstDone) })
	p.logTransitions(st)
}

// fetchAll queries every class with bounded concurrency, slowest first.
func (p *Poller) fetchAll(ctx context.Context) map[string]fetchResult {
	qs := append([]module.Query(nil), p.queries...)
	sort.SliceStable(qs, func(i, j int) bool { return p.lastDur[qs[i].Class] > p.lastDur[qs[j].Class] })

	var (
		mu  sync.Mutex
		out = make(map[string]fetchResult, len(qs))
		wg  sync.WaitGroup
		sem = make(chan struct{}, max(1, p.cfg.MaxConcurrentRequests))
	)
	for _, q := range qs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			out[q.Class] = fetchResult{err: fmt.Errorf("poll timeout before query: %w", ctx.Err())}
			mu.Unlock()
			continue
		}
		wg.Go(func() {
			defer func() { <-sem }()
			rctx, cancel := context.WithTimeout(ctx, p.cfg.RequestTimeout)
			defer cancel()
			t0 := time.Now()
			objs, err := p.sess.ResolveClass(rctx, q.Class, q.Filter, q.KeepFunc())
			mu.Lock()
			out[q.Class] = fetchResult{objs: objs, err: err, dur: time.Since(t0)}
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// logTransitions logs changes in poll and class health, not repetitions.
func (p *Poller) logTransitions(st *State) {
	if st.Result != p.lastRes {
		attrs := []any{"result", st.Result, "duration", st.PollEnd.Sub(st.PollStart)}
		switch {
		case st.Err != nil:
			p.log.Warn("poll failed", append(attrs, "err", st.Err)...)
		case st.Result == ResultSuccess && p.lastRes != "":
			p.log.Info("poll recovered", attrs...)
		case st.Result == ResultSuccess:
			p.log.Info("first poll complete", append(attrs, "ucsm_version", st.Session.Version)...)
		default:
			p.log.Warn("poll incomplete", attrs...)
		}
		p.lastRes = st.Result
	}
	if st.Err != nil {
		return
	}
	for class, cs := range st.Classes {
		prev, had := p.lastErrs[class]
		switch {
		case cs.Err != nil && (!had || prev != cs.Err.Error()):
			p.log.Warn("class query failed", "class", class, "stale", cs.Stale, "err", cs.Err)
			p.lastErrs[class] = cs.Err.Error()
		case cs.Err == nil && had:
			p.log.Info("class query recovered", "class", class)
			delete(p.lastErrs, class)
		}
	}
	if st.RenderErr != nil {
		p.log.Warn("some metrics were rejected", "err", st.RenderErr)
	}
	for name, ms := range st.Modules {
		if !ms.OK() {
			p.log.Error("module failed", "module", name, "err", ms.Err)
		}
	}
}
