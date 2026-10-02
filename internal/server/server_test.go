// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/poller"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
	"github.com/damienstuart/ucs-exporter/internal/ucsm/ucsmtest"
)

type blades struct{}

var cpus = module.NewTable("computeBlade", "ucs_test_blade", []string{"server"}, module.G("numOfCpus", "cpus", "CPUs"))

func (blades) Name() string                        { return "blades" }
func (blades) Description() string                 { return "test" }
func (blades) Queries() []module.Query             { return []module.Query{{Class: "computeBlade"}} }
func (blades) Describe(ch chan<- *prometheus.Desc) { cpus.Describe(ch) }
func (blades) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, mo := range s.Class("computeBlade") {
		cpus.Emit(e, mo, strings.TrimPrefix(mo.DN, "sys/"))
	}
	return nil
}

func setup(t *testing.T, cfgYAML string, delay time.Duration, lifecycle bool) (*Server, *poller.Manager) {
	t.Helper()
	fake, err := ucsmtest.New(ucsmtest.WithUser("mon", "pw"),
		ucsmtest.WithObjects(ucsm.NewMO("computeBlade", "sys/chassis-1/blade-1", "numOfCpus", "2")))
	if err != nil {
		t.Fatal(err)
	}
	if delay > 0 {
		fake.DelayClass("computeBlade", delay)
	}
	cfg, err := config.Parse([]byte(cfgYAML), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m, err := poller.NewManager(ctx, cfg, poller.ManagerOptions{
		Build:     func(config.Resolved) ([]module.Module, error) { return []module.Module{blades{}}, nil },
		Transport: fake.RoundTripper(),
		NoJitter:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); m.Stop(); m.Wait() })
	s, err := New(Options{Manager: m, FirstPollWait: 2 * time.Second, EnableLifecycle: lifecycle, Reload: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return s, m
}

const cfg = `
defaults: {username: mon, password: pw}
domains: [{name: ucs1}]
unlisted_domains: {enabled: true, allow: ['lab-[0-9]'], max_domains: 1}
`

func get(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestMetricsEndpoints(t *testing.T) {
	s, _ := setup(t, cfg, 0, false)

	rec := get(t, s, "/metrics?domain=UCS1")
	if rec.Code != 200 {
		t.Fatalf("domain scrape = %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`ucs_up{domain="ucs1"} 1`, `ucs_test_blade_cpus{domain="ucs1",server="chassis-1/blade-1"} 2`} {
		if !strings.Contains(body, want) {
			t.Errorf("domain scrape missing %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "go_goroutines") {
		t.Error("domain scrape includes exporter self-metrics")
	}

	rec = get(t, s, "/metrics")
	body = rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "ucs_exporter_build_info") || !strings.Contains(body, `ucs_exporter_domains{source="static"} 1`) {
		t.Errorf("self scrape = %d\n%s", rec.Code, body)
	}
	if strings.Contains(body, "ucs_up") {
		t.Error("self scrape includes domain metrics")
	}

	for target, code := range map[string]int{
		"/metrics?domain=":             400,
		"/metrics?domain=a&domain=b":   400,
		"/metrics?domain=nope.example": 404,
		"/metrics?domain=lab-12":       404,
		"/healthz":                     200,
		"/-/healthy":                   200,
		"/-/ready":                     200,
		"/-/reload":                    403,
		"/status":                      200,
		"/":                            200,
	} {
		if rec := get(t, s, target); rec.Code != code {
			t.Errorf("GET %s = %d, want %d (%s)", target, rec.Code, code, rec.Body)
		}
	}
	if body := get(t, s, "/status").Body.String(); !strings.Contains(body, "ucs1") || !strings.Contains(body, "success") {
		t.Errorf("status page:\n%s", body)
	}
	if body := get(t, s, "/metrics").Body.String(); !strings.Contains(body, `ucs_exporter_unlisted_domain_rejections_total{reason="not_allowed"} 2`) {
		t.Errorf("rejections not counted:\n%s", body)
	}
	// The 404 says why the domain was rejected.
	const want = `unknown domain "nope.example": not listed under domains, and it does not match any unlisted_domains.allow pattern`
	if body := get(t, s, "/metrics?domain=nope.example").Body.String(); !strings.Contains(body, want) {
		t.Errorf("404 body = %q, want %q", body, want)
	}
}

func TestFirstPollPending(t *testing.T) {
	s, _ := setup(t, cfg, 10*time.Second, false)
	req := httptest.NewRequest(http.MethodGet, "/metrics?domain=ucs1", nil)
	req.Header.Set("X-Prometheus-Scrape-Timeout-Seconds", "1")
	rec := httptest.NewRecorder()
	start := time.Now()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "60" {
		t.Errorf("pending scrape = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if d := time.Since(start); d < 400*time.Millisecond || d > 1500*time.Millisecond {
		t.Errorf("waited %v, want about 0.5s", d)
	}
	if rec := get(t, s, "/-/ready"); rec.Code != 503 {
		t.Errorf("ready = %d while first poll pending", rec.Code)
	}
}

func TestUnlistedAndReload(t *testing.T) {
	s, m := setup(t, cfg, 0, true)
	if rec := get(t, s, "/metrics?domain=lab-1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `ucs_up{domain="lab-1"} 1`) {
		t.Errorf("unlisted scrape = %d\n%s", rec.Code, rec.Body)
	}
	if rec := get(t, s, "/metrics?domain=lab-2"); rec.Code != 503 {
		t.Errorf("over limit = %d", rec.Code)
	}
	if _, ul := m.Counts(); ul != 1 {
		t.Errorf("unlisted = %d", ul)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/-/reload", nil))
	if rec.Code != 200 {
		t.Errorf("reload = %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, s, "/-/reload"); rec.Code != 405 {
		t.Errorf("GET reload = %d", rec.Code)
	}
}
