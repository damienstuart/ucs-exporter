// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
	"github.com/damienstuart/ucs-exporter/internal/ucsm/ucsmtest"
)

var bladeObjects = []*ucsm.MO{
	ucsm.NewMO("computeBlade", "sys/chassis-1/blade-1", "numOfCpus", "2", "operState", "ok"),
	ucsm.NewMO("computeBlade", "sys/chassis-1/blade-2", "numOfCpus", "2", "operState", "unassociated"),
}

func newFake(t *testing.T, opts ...ucsmtest.Option) *ucsmtest.Server {
	t.Helper()
	opts = append([]ucsmtest.Option{ucsmtest.WithUser("mon", `p<&>"'x`), ucsmtest.WithObjects(bladeObjects...)}, opts...)
	s, err := ucsmtest.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type events struct {
	mu sync.Mutex
	m  map[string]int
}

func (e *events) add(op, result string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.m == nil {
		e.m = map[string]int{}
	}
	e.m[op+"/"+result]++
}

func (e *events) get(k string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.m[k]
}

func newSession(t *testing.T, s *ucsmtest.Server, pass string, ev *events) *ucsm.Session {
	t.Helper()
	c, err := ucsm.NewClient(ucsm.ClientConfig{Address: "ucs.test", Transport: s.RoundTripper()})
	if err != nil {
		t.Fatal(err)
	}
	o := ucsm.SessionOptions{}
	if ev != nil {
		o.OnEvent = ev.add
	}
	return ucsm.NewSession(c, ucsm.StaticCredentials{User: "mon", Password: pass}, o)
}

const goodPass = `p<&>"'x`

func TestSessionLoginAndQuery(t *testing.T) {
	s := newFake(t)
	sess := newSession(t, s, goodPass, nil)
	ctx := context.Background()
	for range 2 {
		mos, err := sess.ResolveClass(ctx, "computeBlade", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(mos) != 2 || mos[0].Get("numOfCpus") != "2" {
			t.Fatalf("objects = %+v", mos)
		}
	}
	if n := s.Count("aaaLogin"); n != 1 {
		t.Errorf("logins = %d, want 1", n)
	}
	if v := sess.Info().Version; v != "4.3(4a)" {
		t.Errorf("version = %q", v)
	}

	// Server-side filtering.
	mos, err := sess.ResolveClass(ctx, "computeBlade", ucsm.Eq("computeBlade", "operState", "ok"), nil)
	if err != nil || len(mos) != 1 || mos[0].DN != "sys/chassis-1/blade-1" {
		t.Errorf("filtered = %+v, %v", mos, err)
	}
	// Unknown classes return no objects.
	if mos, err := sess.ResolveClass(ctx, "noSuchClass", nil, nil); err != nil || len(mos) != 0 {
		t.Errorf("unknown class = %+v, %v", mos, err)
	}
}

func TestSessionReauthOn552(t *testing.T) {
	s := newFake(t)
	ev := &events{}
	sess := newSession(t, s, goodPass, ev)
	ctx := context.Background()
	if err := sess.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	s.ExpireSessions()
	if _, err := sess.ResolveClass(ctx, "computeBlade", nil, nil); err != nil {
		t.Fatalf("query after expiry: %v", err)
	}
	if n := s.Count("aaaLogin"); n != 2 {
		t.Errorf("logins = %d, want 2", n)
	}
	if n := ev.get("reauth/success"); n != 1 {
		t.Errorf("reauth events = %d, want 1", n)
	}
}

func TestSessionReauthSingleFlight(t *testing.T) {
	s := newFake(t)
	sess := newSession(t, s, goodPass, nil)
	ctx := context.Background()
	if err := sess.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	s.ExpireSessions()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			if _, err := sess.ResolveClass(ctx, "computeBlade", nil, nil); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := s.Count("aaaLogin"); n != 2 {
		t.Errorf("logins = %d, want 2 (one re-login shared by all callers)", n)
	}
}

func TestSessionRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFake(t, ucsmtest.WithRefreshPeriod(60))
		sess := newSession(t, s, goodPass, nil)
		ctx := context.Background()
		if err := sess.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		due := sess.RefreshDue()
		if d := time.Until(due); d != 30*time.Second {
			t.Errorf("refresh due in %v, want 30s", d)
		}
		time.Sleep(20 * time.Second)
		if err := sess.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		if n := s.Count("aaaRefresh"); n != 0 {
			t.Errorf("refreshed early (%d)", n)
		}
		time.Sleep(15 * time.Second)
		if err := sess.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		if n := s.Count("aaaRefresh"); n != 1 {
			t.Errorf("refreshes = %d, want 1", n)
		}
		// The refreshed cookie works and the old one was retired.
		if _, err := sess.ResolveClass(ctx, "computeBlade", nil, nil); err != nil {
			t.Fatal(err)
		}
		if s.Sessions() != 1 || s.Count("aaaLogin") != 1 {
			t.Errorf("sessions = %d, logins = %d; want 1, 1", s.Sessions(), s.Count("aaaLogin"))
		}
	})
}

func TestSessionExpiredCookieRelogin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFake(t, ucsmtest.WithRefreshPeriod(60))
		sess := newSession(t, s, goodPass, nil)
		ctx := context.Background()
		if err := sess.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute) // cookie expires server-side
		if _, err := sess.ResolveClass(ctx, "computeBlade", nil, nil); err != nil {
			t.Fatal(err)
		}
		if n := s.Count("aaaLogin"); n != 2 {
			t.Errorf("logins = %d, want 2", n)
		}
	})
}

func TestSessionRefreshFailureFallsBackToLogin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFake(t, ucsmtest.WithRefreshPeriod(60))
		sess := newSession(t, s, goodPass, nil)
		ctx := context.Background()
		if err := sess.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		s.ExpireSessions()
		time.Sleep(31 * time.Second)
		if err := sess.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		if s.Count("aaaRefresh") != 1 || s.Count("aaaLogin") != 2 {
			t.Errorf("refreshes = %d, logins = %d; want 1, 2", s.Count("aaaRefresh"), s.Count("aaaLogin"))
		}
	})
}

func TestLoginBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFake(t)
		sess := newSession(t, s, "wrong", nil)
		ctx := context.Background()
		err := sess.Ensure(ctx)
		if !ucsm.IsAPIError(err) {
			t.Fatalf("first login err = %v, want API error", err)
		}
		var be *ucsm.LoginBackoffError
		if err := sess.Ensure(ctx); !errors.As(err, &be) {
			t.Fatalf("second login err = %v, want backoff", err)
		}
		if n := s.Count("aaaLogin"); n != 1 {
			t.Errorf("logins during backoff = %d, want 1", n)
		}
		time.Sleep(61 * time.Second)
		_ = sess.Ensure(ctx)
		if n := s.Count("aaaLogin"); n != 2 {
			t.Errorf("logins after backoff = %d, want 2", n)
		}
		// Backoff doubles.
		time.Sleep(61 * time.Second)
		if err := sess.Ensure(ctx); !errors.As(err, &be) {
			t.Errorf("err = %v, want backoff (2m)", err)
		}
		time.Sleep(60 * time.Second)
		_ = sess.Ensure(ctx)
		if n := s.Count("aaaLogin"); n != 3 {
			t.Errorf("logins = %d, want 3", n)
		}
	})
}

func TestMaxSessions(t *testing.T) {
	s := newFake(t, ucsmtest.WithMaxSessions(1))
	ctx := context.Background()
	if err := newSession(t, s, goodPass, nil).Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	err := newSession(t, s, goodPass, nil).Ensure(ctx)
	if !ucsm.IsMaxSessions(err) {
		t.Errorf("err = %v, want max sessions", err)
	}
}

func TestLogout(t *testing.T) {
	s := newFake(t)
	sess := newSession(t, s, goodPass, nil)
	ctx := context.Background()
	if err := sess.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sess.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Sessions() != 0 || sess.LoggedIn() {
		t.Errorf("sessions = %d, logged in = %v after logout", s.Sessions(), sess.LoggedIn())
	}
	if err := sess.Logout(ctx); err != nil || s.Count("aaaLogout") != 1 {
		t.Errorf("second logout: %v, logouts = %d", err, s.Count("aaaLogout"))
	}
	// A session UCSM already dropped (555) counts as logged out.
	if err := sess.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	s.ExpireSessions()
	if err := sess.Logout(ctx); err != nil {
		t.Errorf("logout of unknown session: %v", err)
	}
}

func TestAPIErrorPassThrough(t *testing.T) {
	s := newFake(t)
	s.UnknownClass("swCardEnvStats")
	sess := newSession(t, s, goodPass, nil)
	_, err := sess.ResolveClass(context.Background(), "swCardEnvStats", nil, nil)
	var ae *ucsm.APIError
	if !errors.As(err, &ae) || ae.Code != "ERR-xml-parse-error" || !strings.Contains(err.Error(), "no class named swCardEnvStats") {
		t.Errorf("err = %v", err)
	}
}

func TestContextCancel(t *testing.T) {
	s := newFake(t)
	s.DelayClass("computeBlade", time.Hour)
	sess := newSession(t, s, goodPass, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := sess.ResolveClass(ctx, "computeBlade", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("cancellation took %v", d)
	}
}

func TestHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nuova" {
			http.Redirect(w, r, "https://ucs-primary.example.com/nuova", http.StatusFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()
	ctx := context.Background()

	c, _ := ucsm.NewClient(ucsm.ClientConfig{Address: srv.URL})
	_, err := c.Do(ctx, ucsm.LoginRequest("u", "p"), ucsm.DecodeOptions{})
	var he *ucsm.HTTPStatusError
	if !errors.As(err, &he) || he.Status != http.StatusFound || he.Location != "https://ucs-primary.example.com/nuova" {
		t.Errorf("redirect err = %v", err)
	}

	c, _ = ucsm.NewClient(ucsm.ClientConfig{Address: srv.URL + "/other"})
	_, err = c.Do(ctx, ucsm.LoginRequest("u", "p"), ucsm.DecodeOptions{})
	if !errors.As(err, &he) || he.Status != 500 || !strings.Contains(err.Error(), "internal error") {
		t.Errorf("500 err = %v", err)
	}
}

func TestResponseTooLarge(t *testing.T) {
	s := newFake(t)
	c, _ := ucsm.NewClient(ucsm.ClientConfig{Address: "ucs.test", Transport: s.RoundTripper(), MaxResponseBytes: 64})
	_, err := c.Do(context.Background(), ucsm.LoginRequest("mon", goodPass), ucsm.DecodeOptions{})
	if !errors.Is(err, ucsm.ErrResponseTooLarge) {
		t.Errorf("err = %v, want ErrResponseTooLarge", err)
	}
}

func TestTLSVerification(t *testing.T) {
	s := newFake(t)
	srv := s.NewTLSServer()
	defer srv.Close()
	ctx := context.Background()

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c, _ := ucsm.NewClient(ucsm.ClientConfig{Address: srv.URL, TLS: &tls.Config{RootCAs: pool}})
	if err := ucsm.NewSession(c, ucsm.StaticCredentials{User: "mon", Password: goodPass}, ucsm.SessionOptions{}).Ensure(ctx); err != nil {
		t.Errorf("login with CA: %v", err)
	}

	c, _ = ucsm.NewClient(ucsm.ClientConfig{Address: srv.URL})
	err := ucsm.NewSession(c, ucsm.StaticCredentials{User: "mon", Password: goodPass}, ucsm.SessionOptions{}).Ensure(ctx)
	var te *ucsm.TransportError
	if !errors.As(err, &te) || !strings.Contains(err.Error(), "tls.ca_file") {
		t.Errorf("untrusted cert err = %v, want hint", err)
	}
}

func TestTrace(t *testing.T) {
	s := newFake(t)
	var mu sync.Mutex
	var dumps []string
	c, _ := ucsm.NewClient(ucsm.ClientConfig{Address: "ucs.test", Transport: s.RoundTripper(), Trace: func(dir, method string, body []byte) {
		mu.Lock()
		defer mu.Unlock()
		dumps = append(dumps, dir+" "+method+" "+string(body))
	}})
	sess := ucsm.NewSession(c, ucsm.StaticCredentials{User: "mon", Password: goodPass}, ucsm.SessionOptions{})
	if _, err := sess.ResolveClass(context.Background(), "computeBlade", nil, nil); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(dumps, "\n")
	if strings.Contains(all, "p&lt;&amp;") || strings.Contains(all, "fake-0000") {
		t.Errorf("trace leaked secrets:\n%s", all)
	}
	if len(dumps) != 4 {
		t.Errorf("got %d trace entries, want 4", len(dumps))
	}
}

// A caller whose cookie was rejected while another caller's re-login failed
// must not send a request with an empty cookie.
func TestReauthAfterFailedRelogin(t *testing.T) {
	s := newFake(t)
	sess := newSession(t, s, goodPass, nil)
	ctx := context.Background()
	if err := sess.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	s.ExpireSessions()
	s.FailLogins(1)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _ = sess.ResolveClass(ctx, "computeBlade", nil, nil) })
	}
	wg.Wait()
	for _, r := range s.Requests() {
		if r.Method == "configResolveClass" && r.Cookie == "" {
			t.Fatal("request sent with an empty cookie")
		}
	}
}

// A transient refresh failure must not fail callers while the cookie is
// still valid.
func TestTransientRefreshFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFake(t, ucsmtest.WithRefreshPeriod(60))
		sess := newSession(t, s, goodPass, nil)
		ctx := context.Background()
		if err := sess.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(31 * time.Second)
		s.FailHTTP("aaaRefresh", 1)
		if err := sess.Ensure(ctx); err != nil {
			t.Fatalf("Ensure after transient refresh failure: %v", err)
		}
		if _, err := sess.ResolveClass(ctx, "computeBlade", nil, nil); err != nil {
			t.Fatal(err)
		}
		// The next Ensure retries the refresh and succeeds.
		if err := sess.Ensure(ctx); err != nil || s.Count("aaaRefresh") != 2 || s.Count("aaaLogin") != 1 {
			t.Errorf("err=%v refreshes=%d logins=%d", err, s.Count("aaaRefresh"), s.Count("aaaLogin"))
		}
	})
}
