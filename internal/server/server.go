// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Package server implements the exporter's HTTP endpoints.
package server

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/poller"
)

// Options configures the server.
type Options struct {
	Manager *poller.Manager
	Logger  *slog.Logger
	// EnableLifecycle enables POST /-/reload.
	EnableLifecycle bool
	// Reload reloads the configuration.
	Reload func() error
	// FirstPollWait bounds how long a scrape waits for a domain's first
	// poll (default 10s; also bounded by the Prometheus scrape timeout).
	FirstPollWait time.Duration
}

// Server serves /metrics and the auxiliary endpoints.
type Server struct {
	o           Options
	log         *slog.Logger
	self        *prometheus.Registry
	selfHandler http.Handler
	scrapes     *prometheus.CounterVec
	rejections  *prometheus.CounterVec
	reloadOK    prometheus.Gauge
	reloadTS    prometheus.Gauge
	mux         *http.ServeMux
}

// New creates the server.
func New(o Options) (*Server, error) {
	if o.FirstPollWait <= 0 {
		o.FirstPollWait = 10 * time.Second
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{o: o, log: log, self: prometheus.NewRegistry(), mux: http.NewServeMux()}

	s.scrapes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ucs_exporter_domain_scrapes_total",
		Help: "Scrapes of /metrics?domain=, by HTTP status code.",
	}, []string{"code"})
	s.rejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ucs_exporter_unlisted_domain_rejections_total",
		Help: "Requests for domains that are not configured, by reason (not_allowed, limit).",
	}, []string{"reason"})
	s.reloadOK = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ucs_exporter_config_last_reload_successful",
		Help: "Whether the last configuration reload succeeded.",
	})
	s.reloadTS = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ucs_exporter_config_last_reload_success_timestamp_seconds",
		Help: "Time of the last successful configuration load.",
	})
	s.reloadOK.Set(1)
	s.reloadTS.SetToCurrentTime()
	domains := func(unlisted bool) func() float64 {
		return func() float64 {
			st, ul := o.Manager.Counts()
			if unlisted {
				return float64(ul)
			}
			return float64(st)
		}
	}
	for _, c := range []prometheus.Collector{
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		versioncollector.NewCollector("ucs_exporter"),
		s.scrapes, s.rejections, s.reloadOK, s.reloadTS,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "ucs_exporter_domains", Help: "Domains being polled, by source.",
			ConstLabels: prometheus.Labels{"source": "static"},
		}, domains(false)),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "ucs_exporter_domains", Help: "Domains being polled, by source.",
			ConstLabels: prometheus.Labels{"source": "unlisted"},
		}, domains(true)),
	} {
		if err := s.self.Register(c); err != nil {
			return nil, err
		}
	}
	s.selfHandler = promhttp.InstrumentMetricHandler(s.self, promhttp.HandlerFor(s.self, promhttp.HandlerOpts{
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
		ErrorHandling:     promhttp.ContinueOnError,
		EnableOpenMetrics: true,
	}))

	landing, err := web.NewLandingPage(web.LandingConfig{
		Name:        "UCS Exporter",
		Description: "Prometheus exporter for Cisco UCS Manager",
		Version:     version.Info(),
		Profiling:   "false",
		Form: web.LandingForm{
			Action: "metrics",
			Inputs: []web.LandingFormInput{{Label: "Domain", Type: "text", Name: "domain", Placeholder: "ucs.example.com"}},
		},
		Links: []web.LandingLinks{
			{Address: "/metrics", Text: "Exporter metrics", Description: "Metrics about the exporter itself"},
			{Address: "/status", Text: "Status", Description: "Poll status of each domain"},
		},
	})
	if err != nil {
		return nil, err
	}

	s.mux.Handle("/", landing)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	s.mux.HandleFunc("/status", s.handleStatus)
	ok := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("OK")) }
	s.mux.HandleFunc("/healthz", ok)
	s.mux.HandleFunc("/-/healthy", ok)
	s.mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
		if !o.Manager.Ready() {
			http.Error(w, "waiting for first polls", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("OK"))
	})
	s.mux.HandleFunc("/-/reload", s.handleReload)
	return s, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// ReloadResult records the outcome of a configuration reload.
func (s *Server) ReloadResult(err error) {
	if err != nil {
		s.reloadOK.Set(0)
		return
	}
	s.reloadOK.Set(1)
	s.reloadTS.SetToCurrentTime()
}

func (s *Server) fail(w http.ResponseWriter, code int, msg string) {
	s.scrapes.WithLabelValues(strconv.Itoa(code)).Inc()
	http.Error(w, msg, code)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	domains, ok := r.URL.Query()["domain"]
	if !ok {
		s.selfHandler.ServeHTTP(w, r)
		return
	}
	if len(domains) != 1 || domains[0] == "" {
		s.fail(w, http.StatusBadRequest, "exactly one non-empty domain parameter is required")
		return
	}
	name := domains[0]
	p, err := s.o.Manager.Get(name)
	switch {
	case errors.Is(err, config.ErrNotAllowed):
		s.rejections.WithLabelValues("not_allowed").Inc()
		s.fail(w, http.StatusNotFound, fmt.Sprintf("unknown domain %q: %v", name, err))
		return
	case errors.Is(err, poller.ErrLimit):
		s.rejections.WithLabelValues("limit").Inc()
		s.fail(w, http.StatusServiceUnavailable, "too many unlisted domains")
		return
	case err != nil:
		s.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.Touch()

	st := p.State()
	if st == nil {
		wait := s.o.FirstPollWait
		if v, err := strconv.ParseFloat(r.Header.Get("X-Prometheus-Scrape-Timeout-Seconds"), 64); err == nil && v > 0 {
			wait = min(wait, time.Duration((v-0.5)*float64(time.Second)))
		}
		ctx, cancel := context.WithTimeout(r.Context(), max(wait, 0))
		err := p.WaitFirst(ctx)
		cancel()
		if err != nil {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(p.Config().Interval.Seconds()))))
			s.fail(w, http.StatusServiceUnavailable, fmt.Sprintf("first poll of domain %q has not completed yet", name))
			return
		}
		st = p.State()
	}

	health := prometheus.NewRegistry()
	if err := health.Register(p.Health(st)); err != nil {
		s.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	fams := st.Families
	g := prometheus.Gatherers{health, prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) { return fams, nil })}
	s.scrapes.WithLabelValues("200").Inc()
	promhttp.HandlerFor(g, promhttp.HandlerOpts{
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelError),
		ErrorHandling:     promhttp.ContinueOnError,
		EnableOpenMetrics: true,
	}).ServeHTTP(w, r)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if !s.o.EnableLifecycle {
		http.Error(w, "lifecycle API is not enabled (--web.enable-lifecycle)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if s.o.Reload == nil {
		http.Error(w, "reload not supported", http.StatusNotImplemented)
		return
	}
	if err := s.o.Reload(); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write([]byte("OK"))
}

type statusRow struct {
	Name, Source, Result, Version, Err string
	Age                                string
	Duration                           string
	Failing                            []string
}

var statusTmpl = template.Must(template.New("status").Parse(`<!DOCTYPE html>
<html><head><title>UCS Exporter status</title>
<style>body{font-family:sans-serif}table{border-collapse:collapse}td,th{border:1px solid #ccc;padding:4px 8px;text-align:left;vertical-align:top}</style>
</head><body><h1>UCS Exporter status</h1>
<table><tr><th>Domain</th><th>Source</th><th>Last poll</th><th>Age</th><th>Duration</th><th>UCSM</th><th>Failing classes</th><th>Error</th></tr>
{{range .}}<tr><td><a href="metrics?domain={{.Name}}">{{.Name}}</a></td><td>{{.Source}}</td><td>{{.Result}}</td><td>{{.Age}}</td><td>{{.Duration}}</td><td>{{.Version}}</td><td>{{range .Failing}}{{.}}<br>{{end}}</td><td>{{.Err}}</td></tr>
{{end}}</table></body></html>
`))

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	var rows []statusRow
	for _, p := range s.o.Manager.Pollers() {
		row := statusRow{Name: p.Name(), Source: "static", Result: "pending"}
		if p.Config().Unlisted {
			row.Source = "unlisted"
		}
		if st := p.State(); st != nil {
			row.Result = st.Result
			row.Age = time.Since(st.PollEnd).Truncate(time.Second).String()
			row.Duration = st.PollEnd.Sub(st.PollStart).Truncate(time.Millisecond).String()
			row.Version = st.Session.Version
			if st.Err != nil {
				row.Err = st.Err.Error()
			}
			for class, cs := range st.Classes {
				if cs.Err != nil && st.Err == nil {
					row.Failing = append(row.Failing, class+": "+cs.Err.Error())
				}
			}
		}
		rows = append(rows, row)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := statusTmpl.Execute(w, rows); err != nil {
		s.log.Error("rendering status page", "err", err)
	}
}
