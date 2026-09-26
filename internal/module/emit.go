// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package module

import (
	"fmt"
	"slices"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

const maxErrors = 20

// Emitter collects the metrics produced by one module for one snapshot.
// Series with an empty label value are dropped (and counted) rather than
// exported, because an empty label is indistinguishable from an absent one
// and usually means an object could not be identified.
type Emitter struct {
	metrics []prometheus.Metric
	dropped map[string]int
	errs    []string
}

// NewEmitter returns an empty emitter.
func NewEmitter() *Emitter { return &Emitter{} }

// Emit adds a metric.
func (e *Emitter) Emit(d *prometheus.Desc, t prometheus.ValueType, v float64, labelValues ...string) {
	for _, l := range labelValues {
		if l == "" {
			e.Drop("empty_label")
			return
		}
	}
	m, err := prometheus.NewConstMetric(d, t, v, labelValues...)
	if err != nil {
		e.Drop("invalid")
		e.Errorf("%v", err)
		return
	}
	e.metrics = append(e.metrics, m)
}

// Gauge adds a gauge.
func (e *Emitter) Gauge(d *prometheus.Desc, v float64, labelValues ...string) {
	e.Emit(d, prometheus.GaugeValue, v, labelValues...)
}

// Counter adds a counter.
func (e *Emitter) Counter(d *prometheus.Desc, v float64, labelValues ...string) {
	e.Emit(d, prometheus.CounterValue, v, labelValues...)
}

// Bool adds a 0/1 gauge.
func (e *Emitter) Bool(d *prometheus.Desc, b bool, labelValues ...string) {
	v := 0.0
	if b {
		v = 1
	}
	e.Gauge(d, v, labelValues...)
}

// Info adds an info metric (value 1).
func (e *Emitter) Info(d *prometheus.Desc, labelValues ...string) {
	e.Gauge(d, 1, labelValues...)
}

// State adds a state metric: value 1 with the raw UCSM enum value as the
// last label ("state"). An empty state is reported as "unknown".
func (e *Emitter) State(d *prometheus.Desc, state string, labelValues ...string) {
	e.Gauge(d, 1, slices.Concat(labelValues, []string{OrUnknown(state)})...)
}

// Attr adds a metric from a numeric attribute of mo, multiplied by scale (0
// means 1). It reports false, and emits nothing, if the attribute is missing
// or not a number.
func (e *Emitter) Attr(d *prometheus.Desc, t prometheus.ValueType, mo *ucsm.MO, attr string, scale float64, labelValues ...string) bool {
	v, ok := mo.Float(attr)
	if !ok {
		return false
	}
	if scale != 0 {
		v *= scale
	}
	e.Emit(d, t, v, labelValues...)
	return true
}

// Drop records an object or series that was skipped, by reason.
func (e *Emitter) Drop(reason string) {
	if e.dropped == nil {
		e.dropped = map[string]int{}
	}
	e.dropped[reason]++
}

// Errorf records a non-fatal problem.
func (e *Emitter) Errorf(format string, args ...any) {
	if len(e.errs) < maxErrors {
		e.errs = append(e.errs, fmt.Sprintf(format, args...))
	}
}

// Metrics returns the collected metrics.
func (e *Emitter) Metrics() []prometheus.Metric { return e.metrics }

// Dropped returns the drop counts by reason.
func (e *Emitter) Dropped() map[string]int { return e.dropped }

// Errors returns the recorded non-fatal problems.
func (e *Emitter) Errors() []string { return e.errs }

// NewDesc is shorthand for a descriptor without constant labels.
func NewDesc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, labels, nil)
}

// OrUnknown returns v, or "unknown" if v is empty. Use it for descriptive
// (non-identity) label values.
func OrUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
