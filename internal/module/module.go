// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Package module defines how metric modules plug into the exporter.
//
// A module declares the UCSM classes it needs (Queries) and turns a Snapshot
// of those classes into Prometheus metrics (Collect). The poller queries the
// union of all enabled modules' classes once per interval, builds a Snapshot
// and renders every module into an immutable set of metric families that is
// served until the next poll.
package module

import (
	"slices"
	"sort"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// Query is a UCSM class a module needs.
type Query struct {
	Class string
	// Filter is an optional server-side filter. Several modules may query
	// the same class with different filters, in which case the filter is
	// dropped, so modules must not rely on it for correctness.
	Filter ucsm.Filter
	// Attrs lists the attributes the module reads. nil means all. Decoding
	// drops other attributes, which matters for *Stats classes that carry
	// dozens of unused Delta/Avg/Min/Max attributes.
	Attrs []string
}

// Module produces metrics from a Snapshot.
type Module interface {
	// Name is the configuration key, e.g. "fc_extended".
	Name() string
	// Description is a one-line summary for documentation.
	Description() string
	// Queries lists the classes the module needs.
	Queries() []Query
	// Describe sends every metric descriptor the module can emit. It is
	// used for documentation and to detect name clashes between modules.
	Describe(ch chan<- *prometheus.Desc)
	// Collect emits metrics for the snapshot. Missing classes must be
	// tolerated (they yield no objects).
	Collect(s *Snapshot, e *Emitter) error
}

// MergeQueries combines the queries of several modules: one query per
// class, with the union of attributes (nil if any module needs all) and the
// filter kept only if every module uses the same one.
func MergeQueries(mods []Module) []Query {
	type merged struct {
		q        Query
		allAttrs bool
		attrs    map[string]bool
		filter   string
		mixed    bool
	}
	byClass := map[string]*merged{}
	for _, m := range mods {
		for _, q := range m.Queries() {
			fs := ""
			if q.Filter != nil {
				fs = q.Filter.String()
			}
			cur, ok := byClass[q.Class]
			if !ok {
				cur = &merged{q: Query{Class: q.Class, Filter: q.Filter}, attrs: map[string]bool{}, filter: fs}
				byClass[q.Class] = cur
			} else if cur.filter != fs {
				cur.mixed = true
			}
			if q.Attrs == nil {
				cur.allAttrs = true
			}
			for _, a := range q.Attrs {
				cur.attrs[a] = true
			}
		}
	}
	out := make([]Query, 0, len(byClass))
	for _, m := range byClass {
		q := m.q
		if m.mixed {
			q.Filter = nil
		}
		if !m.allAttrs {
			q.Attrs = make([]string, 0, len(m.attrs))
			for a := range m.attrs {
				q.Attrs = append(q.Attrs, a)
			}
			sort.Strings(q.Attrs)
		}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

// KeepFunc returns a decode projection for the query's attributes, or nil
// if all attributes are needed.
func (q Query) KeepFunc() func(class, attr string) bool {
	if q.Attrs == nil {
		return nil
	}
	keep := make(map[string]bool, len(q.Attrs))
	for _, a := range q.Attrs {
		keep[a] = true
	}
	return func(_, attr string) bool { return keep[attr] }
}

// Classes returns the sorted, de-duplicated class IDs of the queries.
func Classes(qs []Query) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.Class)
	}
	sort.Strings(out)
	return slices.Compact(out)
}
