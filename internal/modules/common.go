// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Package modules implements the exporter's metric modules.
//
// Naming conventions: every metric starts with ucs_, counters come from
// UCSM's cumulative attributes (never the per-interval *Delta ones) and end
// in _total, values use base units (bytes, seconds, celsius, watts), enum
// attributes are exported as "<name>_state{state="<raw value>"} 1" and each
// entity with health has exactly one derived 0/1 gauge (_up for
// connectivity, _ok for operability). Identity labels come from the DN and
// are never empty; descriptive attributes live in _info metrics.
package modules

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
)

// Builtin returns every module, configured with opts, in documentation
// order.
func Builtin(opts config.Options) []module.Module {
	return []module.Module{
		newSystem(),
		newCapacity(),
		newPower(),
		newCPUTemperature(),
		newFans(),
		newFISystem(),
		newMemoryErrors(),
		newFaults(),
		newEthernet(),
		newFC(),
		newVnic(),
		newVirtual(opts),
		newFCExtended(),
		newEthernetExtended(opts),
		newHardware(opts),
	}
}

// Names returns the names of all modules.
func Names() []string {
	var names []string
	for _, m := range Builtin(config.Options{}) {
		names = append(names, m.Name())
	}
	return names
}

// Build returns the modules enabled for a domain: all of them unless the
// configuration lists specific modules.
func Build(r config.Resolved) ([]module.Module, error) {
	all := Builtin(r.Options)
	if r.Modules == nil {
		return all, nil
	}
	byName := map[string]module.Module{}
	for _, m := range all {
		byName[m.Name()] = m
	}
	var out []module.Module
	for _, name := range r.Modules {
		m, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown module %q (known: %v)", name, Names())
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

// Validate checks the module names in a configuration.
func Validate(cfg *config.Config) error {
	known := Names()
	check := func(where string, names []string) error {
		for _, n := range names {
			if !slices.Contains(known, n) {
				return fmt.Errorf("%s: unknown module %q (known: %v)", where, n, known)
			}
		}
		return nil
	}
	var errs []error
	if err := check("defaults.modules", cfg.Defaults.Modules); err != nil {
		errs = append(errs, err)
	}
	for _, d := range cfg.Domains {
		if err := check(fmt.Sprintf("domain %q: modules", d.Name), d.Modules); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// base implements the bookkeeping shared by all modules.
type base struct {
	name, description string
	queries           []module.Query
	descs             []*prometheus.Desc
	tables            []*module.Table
}

func (b *base) Name() string            { return b.name }
func (b *base) Description() string     { return b.description }
func (b *base) Queries() []module.Query { return b.queries }
func (b *base) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range b.descs {
		ch <- d
	}
	for _, t := range b.tables {
		t.Describe(ch)
	}
}

// query declares a class and the attributes read from it. Attributes used
// by the module's tables for the class are added automatically by table.
func (b *base) query(class string, attrs ...string) {
	for i := range b.queries {
		if b.queries[i].Class == class {
			b.queries[i].Attrs = mergeAttrs(b.queries[i].Attrs, attrs)
			return
		}
	}
	b.queries = append(b.queries, module.Query{Class: class, Attrs: mergeAttrs(nil, attrs)})
}

func mergeAttrs(a, b []string) []string {
	out := append([]string{}, a...)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// desc creates and records a descriptor.
func (b *base) desc(name, help string, labels ...string) *prometheus.Desc {
	d := module.NewDesc(name, help, labels...)
	b.descs = append(b.descs, d)
	return d
}

// table creates and records a table, declaring its attributes.
func (b *base) table(class, prefix string, labels []string, rows ...module.Row) *module.Table {
	t := module.NewTable(class, prefix, labels, rows...)
	b.tables = append(b.tables, t)
	b.query(class, t.Attrs()...)
	return t
}

// sharedTable creates a table whose rows apply to several classes with the
// same attributes (e.g. computeBlade and computeRackUnit).
func (b *base) sharedTable(classes []string, prefix string, labels []string, rows ...module.Row) *module.Table {
	t := module.NewTable(strings.Join(classes, "/"), prefix, labels, rows...)
	b.tables = append(b.tables, t)
	for _, c := range classes {
		b.query(c, t.Attrs()...)
	}
	return t
}

// kindTables builds one table per entity kind for the same class and rows.
func (b *base) kindTables(class string, kinds []string, rows ...module.Row) map[string]*module.Table {
	out := make(map[string]*module.Table, len(kinds))
	for _, k := range kinds {
		f := families[k]
		out[k] = b.table(class, f.prefix, f.labels, rows...)
	}
	return out
}

// kindDescs builds one descriptor per entity kind: <prefix>_<suffix> with the
// kind's labels plus extra.
func (b *base) kindDescs(kinds []string, suffix, help string, extra ...string) map[string]*prometheus.Desc {
	out := make(map[string]*prometheus.Desc, len(kinds))
	for _, k := range kinds {
		f := families[k]
		out[k] = b.desc(f.prefix+"_"+suffix, help, slices.Concat(f.labels, extra)...)
	}
	return out
}

// containerDescs builds one descriptor per container kind:
// <container prefix>_<suffix> with the container's labels plus extra.
func (b *base) containerDescs(kinds []string, suffix, help string, extra ...string) map[string]*prometheus.Desc {
	out := make(map[string]*prometheus.Desc, len(kinds))
	for _, k := range kinds {
		c := containers[k]
		out[k] = b.desc(c.prefix+"_"+suffix, help, slices.Concat(c.labels, extra)...)
	}
	return out
}
