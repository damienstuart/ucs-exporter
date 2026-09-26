// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultRefreshPeriod is used when UCSM does not report outRefreshPeriod.
const DefaultRefreshPeriod = 600 * time.Second

// Credentials supplies the username and password for a login. Get is called
// for every login and refresh, so a password file can be rotated in place.
type Credentials interface {
	Get() (user, password string, err error)
}

// StaticCredentials is a fixed username and password.
type StaticCredentials struct{ User, Password string }

// Get implements Credentials.
func (c StaticCredentials) Get() (string, string, error) { return c.User, c.Password, nil }

// SessionOptions configures a Session.
type SessionOptions struct {
	// RefreshFraction of outRefreshPeriod after which the cookie is
	// refreshed (default 0.5).
	RefreshFraction float64
	// After a failed login (bad credentials, session limit) further logins
	// are suppressed for BackoffMin, doubling up to BackoffMax, so that a
	// wrong password does not lock out a directory account.
	BackoffMin, BackoffMax time.Duration
	// OnEvent is called with op "login", "refresh", "reauth" or "logout"
	// and result "success" or "failure".
	OnEvent func(op, result string)
	Logger  *slog.Logger
}

// SessionInfo describes the current session. It never contains the cookie.
type SessionInfo struct {
	Version       string // UCSM version (outVersion)
	Priv          string // privileges (outPriv)
	LoggedInAt    time.Time
	RefreshedAt   time.Time
	RefreshPeriod time.Duration
}

// Session is a logged-in UCSM session. It logs in lazily, refreshes the
// cookie before it expires, and transparently logs in again (once) when UCSM
// reports that the cookie is no longer valid. It is safe for concurrent use.
type Session struct {
	c     *Client
	creds Credentials
	o     SessionOptions
	log   *slog.Logger

	// mu guards the fields below. It is held across login and refresh round
	// trips so that concurrent callers share a single login.
	mu          sync.Mutex
	cookie      string
	gen         uint64 // incremented whenever cookie changes
	issued      time.Time
	period      time.Duration
	info        SessionInfo
	nextLogin   time.Time
	backoff     time.Duration
	lastErr     error
	warnedAdmin bool
}

// NewSession returns a session that is not yet logged in.
func NewSession(c *Client, creds Credentials, o SessionOptions) *Session {
	if o.RefreshFraction <= 0 || o.RefreshFraction >= 1 {
		o.RefreshFraction = 0.5
	}
	if o.BackoffMin <= 0 {
		o.BackoffMin = time.Minute
	}
	if o.BackoffMax < o.BackoffMin {
		o.BackoffMax = max(30*time.Minute, o.BackoffMin)
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Session{c: c, creds: creds, o: o, log: log}
}

// Client returns the underlying client.
func (s *Session) Client() *Client { return s.c }

func (s *Session) event(op, result string) {
	if s.o.OnEvent != nil {
		s.o.OnEvent(op, result)
	}
}

// Ensure logs in if there is no session and refreshes the cookie if the
// refresh is due.
func (s *Session) Ensure(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie == "" {
		return s.loginLocked(ctx)
	}
	if !time.Now().Before(s.refreshDueLocked()) {
		err := s.refreshLocked(ctx)
		if err != nil && !IsAPIError(err) && s.cookie != "" && time.Now().Before(s.issued.Add(s.period)) {
			// A transient failure (timeout, HTTP error) while the cookie
			// is still valid: keep using it and refresh next time.
			s.log.Debug("UCSM session refresh failed; the current cookie is still valid", "err", err)
			return nil
		}
		return err
	}
	return nil
}

// Refresh refreshes the cookie now, logging in if there is no session.
func (s *Session) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie == "" {
		return s.loginLocked(ctx)
	}
	return s.refreshLocked(ctx)
}

// RefreshDue returns when the cookie should next be refreshed, or the zero
// time if there is no session.
func (s *Session) RefreshDue() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie == "" {
		return time.Time{}
	}
	return s.refreshDueLocked()
}

func (s *Session) refreshDueLocked() time.Time {
	return s.issued.Add(time.Duration(float64(s.period) * s.o.RefreshFraction))
}

// LoggedIn reports whether the session currently holds a cookie.
func (s *Session) LoggedIn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cookie != ""
}

// Info describes the session.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Logout ends the session. A session UCSM no longer knows about counts as
// logged out.
func (s *Session) Logout(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie == "" {
		return nil
	}
	_, err := s.c.Do(ctx, LogoutRequest(s.cookie), DecodeOptions{})
	s.cookie = ""
	s.gen++
	if err != nil && !IsSessionNotFound(err) {
		s.event("logout", "failure")
		return err
	}
	s.event("logout", "success")
	return nil
}

func (s *Session) loginLocked(ctx context.Context) error {
	if now := time.Now(); now.Before(s.nextLogin) {
		return &LoginBackoffError{Until: s.nextLogin, Last: s.lastErr}
	}
	user, pass, err := s.creds.Get()
	if err != nil {
		s.event("login", "failure")
		return fmt.Errorf("ucsm aaaLogin: reading credentials: %w", err)
	}
	res, err := s.c.Do(ctx, LoginRequest(user, pass), DecodeOptions{})
	if err == nil {
		err = s.acceptLocked(res, true)
	}
	if err != nil {
		s.event("login", "failure")
		if IsAPIError(err) {
			// Bad credentials or session limit: back off. Network errors are
			// retried on the next poll without delay.
			s.backoffLocked(err)
		}
		return err
	}
	s.event("login", "success")
	s.log.Debug("logged in to UCSM", "version", s.info.Version, "refresh_period", s.period)
	return nil
}

func (s *Session) backoffLocked(err error) {
	if s.backoff == 0 {
		s.backoff = s.o.BackoffMin
	} else {
		s.backoff = min(2*s.backoff, s.o.BackoffMax)
	}
	s.nextLogin = time.Now().Add(s.backoff)
	s.lastErr = err
	s.log.Warn("UCSM login failed; suppressing logins to avoid account lockout", "retry_after", s.backoff, "err", err)
}

func (s *Session) refreshLocked(ctx context.Context) error {
	user, pass, err := s.creds.Get()
	if err == nil {
		var res *Result
		res, err = s.c.Do(ctx, RefreshRequest(user, pass, s.cookie), DecodeOptions{})
		if err == nil {
			err = s.acceptLocked(res, false)
		}
	}
	if err == nil {
		s.event("refresh", "success")
		return nil
	}
	s.event("refresh", "failure")
	if !IsAPIError(err) {
		// Keep the cookie; it may still be valid once UCSM is reachable.
		return err
	}
	s.log.Debug("UCSM session refresh failed; logging in again", "err", err)
	s.cookie = ""
	s.gen++
	return s.loginLocked(ctx)
}

func (s *Session) acceptLocked(res *Result, login bool) error {
	cookie := res.Attrs["outCookie"]
	if cookie == "" {
		return fmt.Errorf("ucsm %s: response has no outCookie", res.Method)
	}
	period := DefaultRefreshPeriod
	if v, err := strconv.Atoi(strings.TrimSpace(res.Attrs["outRefreshPeriod"])); err == nil && v > 0 {
		period = time.Duration(v) * time.Second
	}
	s.cookie, s.issued, s.period = cookie, time.Now(), period
	s.gen++
	s.backoff, s.nextLogin, s.lastErr = 0, time.Time{}, nil
	if !login {
		s.info.RefreshedAt, s.info.RefreshPeriod = s.issued, period
		return nil
	}
	s.info = SessionInfo{
		Version:       res.Attrs["outVersion"],
		Priv:          res.Attrs["outPriv"],
		LoggedInAt:    s.issued,
		RefreshPeriod: period,
	}
	if !s.warnedAdmin && hasAdminPriv(s.info.Priv) {
		s.warnedAdmin = true
		s.log.Warn("UCSM account has admin privileges; a read-only account is sufficient for monitoring", "priv", s.info.Priv)
	}
	return nil
}

func hasAdminPriv(priv string) bool {
	for p := range strings.SplitSeq(priv, ",") {
		if strings.TrimSpace(p) == "admin" {
			return true
		}
	}
	return false
}

// withCookie runs fn with a valid cookie. If UCSM rejects the cookie, it logs
// in again once (shared with concurrent callers) and retries fn once.
func (s *Session) withCookie(ctx context.Context, fn func(cookie string) error) error {
	s.mu.Lock()
	if s.cookie == "" {
		if err := s.loginLocked(ctx); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	cookie, gen := s.cookie, s.gen
	s.mu.Unlock()

	err := fn(cookie)
	if !IsAuthRequired(err) {
		return err
	}

	s.mu.Lock()
	if s.gen == gen {
		// Nobody has replaced the rejected cookie yet: log in again.
		s.cookie = ""
		s.gen++
		if lerr := s.loginLocked(ctx); lerr != nil {
			s.mu.Unlock()
			s.event("reauth", "failure")
			return lerr
		}
		s.event("reauth", "success")
	} else if s.cookie == "" {
		// Another caller's re-login failed; try again (subject to the
		// login backoff) rather than sending an empty cookie.
		if lerr := s.loginLocked(ctx); lerr != nil {
			s.mu.Unlock()
			return lerr
		}
	}
	cookie = s.cookie
	s.mu.Unlock()
	return fn(cookie)
}

// Do sends the request built by build (called with the current cookie) and
// decodes the response.
func (s *Session) Do(ctx context.Context, build func(cookie string) Request, opt DecodeOptions) (*Result, error) {
	var res *Result
	err := s.withCookie(ctx, func(cookie string) error {
		var err error
		res, err = s.c.Do(ctx, build(cookie), opt)
		return err
	})
	return res, err
}

// Raw sends the request built by build and returns the raw response body.
func (s *Session) Raw(ctx context.Context, build func(cookie string) Request) ([]byte, error) {
	var raw []byte
	err := s.withCookie(ctx, func(cookie string) error {
		var err error
		raw, err = s.c.DoRaw(ctx, build(cookie))
		return err
	})
	return raw, err
}

// ResolveClass returns all objects of class matching f (which may be nil).
// keep, if non-nil, selects the attributes to retain.
func (s *Session) ResolveClass(ctx context.Context, class string, f Filter, keep func(class, attr string) bool) ([]*MO, error) {
	res, err := s.Do(ctx, func(c string) Request { return ResolveClassRequest(c, class, f, false) }, DecodeOptions{Keep: keep})
	if err != nil {
		return nil, err
	}
	return res.Objects, nil
}

// ResolveClasses returns all objects of the given classes.
func (s *Session) ResolveClasses(ctx context.Context, classes []string, hierarchical bool) ([]*MO, error) {
	res, err := s.Do(ctx, func(c string) Request { return ResolveClassesRequest(c, classes, hierarchical) }, DecodeOptions{})
	if err != nil {
		return nil, err
	}
	return res.Objects, nil
}

// ResolveDn returns the object with the given DN (and its descendants if
// hierarchical).
func (s *Session) ResolveDn(ctx context.Context, dn string, hierarchical bool) ([]*MO, error) {
	res, err := s.Do(ctx, func(c string) Request { return ResolveDnRequest(c, dn, hierarchical) }, DecodeOptions{})
	if err != nil {
		return nil, err
	}
	return res.Objects, nil
}

// ResolveChildren returns the children of dn, optionally restricted to class
// and filtered by f.
func (s *Session) ResolveChildren(ctx context.Context, dn, class string, f Filter, hierarchical bool) ([]*MO, error) {
	res, err := s.Do(ctx, func(c string) Request { return ResolveChildrenRequest(c, dn, class, f, hierarchical) }, DecodeOptions{})
	if err != nil {
		return nil, err
	}
	return res.Objects, nil
}
