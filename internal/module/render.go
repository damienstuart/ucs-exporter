// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package module

import (
	"fmt"
	"io"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// ModuleStatus reports how a module fared when rendering a snapshot.
type ModuleStatus struct {
	Err      string // Collect error or panic; empty on success
	Series   int
	Dropped  map[string]int
	Errors   []string // non-fatal problems
	Duration time.Duration
}

// OK reports whether the module rendered without error.
func (s ModuleStatus) OK() bool { return s.Err == "" }

// Render runs every module over the snapshot and gathers the result into
// validated, sorted metric families. constLabels (e.g. domain) are added to
// every series. The returned error describes metrics that were rejected
// (duplicates, inconsistent labels); the families are still usable.
func Render(s *Snapshot, mods []Module, constLabels prometheus.Labels) ([]*dto.MetricFamily, map[string]ModuleStatus, error) {
	status := make(map[string]ModuleStatus, len(mods))
	var all replay
	for _, m := range mods {
		start := time.Now()
		e := NewEmitter()
		err := safeCollect(m, s, e)
		st := ModuleStatus{Dropped: e.Dropped(), Errors: e.Errors(), Duration: time.Since(start)}
		if err != nil {
			st.Err = err.Error()
		}
		if _, panicked := err.(panicError); !panicked {
			st.Series = len(e.Metrics())
			all = append(all, e.Metrics()...)
		}
		status[m.Name()] = st
	}
	reg := prometheus.NewRegistry()
	if err := prometheus.WrapRegistererWith(constLabels, reg).Register(all); err != nil {
		return nil, status, err
	}
	fams, err := reg.Gather()
	return fams, status, err
}

type panicError struct{ msg string }

func (p panicError) Error() string { return p.msg }

func safeCollect(m Module, s *Snapshot, e *Emitter) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicError{fmt.Sprintf("panic: %v\n%s", r, debug.Stack())}
		}
	}()
	return m.Collect(s, e)
}

// replay is an unchecked collector that re-emits precomputed metrics.
type replay []prometheus.Metric

func (replay) Describe(chan<- *prometheus.Desc) {}

func (r replay) Collect(ch chan<- prometheus.Metric) {
	for _, m := range r {
		ch <- m
	}
}

// WriteText encodes families in the Prometheus text format.
func WriteText(w io.Writer, fams []*dto.MetricFamily) error {
	enc := expfmt.NewEncoder(w, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, f := range fams {
		if err := enc.Encode(f); err != nil {
			return err
		}
	}
	return nil
}
