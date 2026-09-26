// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package module

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// MiB converts UCSM "MB" values, which are mebibytes, to bytes.
const MiB = 1 << 20

// Row maps one numeric attribute of a class to a metric.
type Row struct {
	Attr   string
	Suffix string // appended to the table prefix with "_"
	Help   string
	Type   prometheus.ValueType
	Scale  float64                      // multiplier; 0 means 1
	Parse  func(string) (float64, bool) // nil means ucsm.ParseNumber
}

// C is a counter row: a cumulative UCSM attribute exported as-is.
func C(attr, suffix, help string) Row {
	return Row{Attr: attr, Suffix: suffix, Help: help, Type: prometheus.CounterValue}
}

// G is a gauge row.
func G(attr, suffix, help string) Row {
	return Row{Attr: attr, Suffix: suffix, Help: help, Type: prometheus.GaugeValue}
}

// Scaled returns r with a multiplier.
func (r Row) Scaled(f float64) Row { r.Scale = f; return r }

// Table is a set of rows for one class, exported under one metric prefix
// with one label set.
type Table struct {
	class string
	rows  []Row
	descs []*prometheus.Desc
}

// NewTable builds metric descriptors named prefix_suffix for each row. The
// help text records the source class and attribute.
func NewTable(class, prefix string, labels []string, rows ...Row) *Table {
	t := &Table{class: class, rows: rows}
	for _, r := range rows {
		help := fmt.Sprintf("%s (UCSM %s.%s).", r.Help, class, r.Attr)
		t.descs = append(t.descs, prometheus.NewDesc(prefix+"_"+r.Suffix, help, labels, nil))
	}
	return t
}

// Class returns the table's class ID.
func (t *Table) Class() string { return t.class }

// Attrs returns the attributes the table reads.
func (t *Table) Attrs() []string {
	out := make([]string, len(t.rows))
	for i, r := range t.rows {
		out[i] = r.Attr
	}
	return out
}

// Describe sends the table's descriptors.
func (t *Table) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range t.descs {
		ch <- d
	}
}

// Emit emits every row for which mo has a numeric value.
func (t *Table) Emit(e *Emitter, mo *ucsm.MO, labelValues ...string) {
	for i, r := range t.rows {
		raw, ok := mo.Lookup(r.Attr)
		if !ok {
			continue
		}
		parse := r.Parse
		if parse == nil {
			parse = ucsm.ParseNumber
		}
		v, ok := parse(raw)
		if !ok {
			continue
		}
		if r.Scale != 0 {
			v *= r.Scale
		}
		e.Emit(t.descs[i], r.Type, v, labelValues...)
	}
}
