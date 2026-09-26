// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
)

// Filter is a UCSM query filter, sent as the <inFilter> of a resolve
// request. Filters can also be evaluated locally with Match, which the fake
// UCSM and callers that want to re-check server-side filtering use.
type Filter interface {
	element() Element
	// Match evaluates the filter against mo.
	Match(mo *MO) bool
	// String returns a canonical representation, usable as a map key.
	String() string
}

type propFilter struct {
	op, class, prop, value string
	re                     *regexp.Regexp // wcard only
}

func newProp(op, class, prop, value string) propFilter {
	return propFilter{op: op, class: class, prop: prop, value: value}
}

// Eq matches objects whose property equals value.
func Eq(class, prop, value string) Filter { return newProp("eq", class, prop, value) }

// Ne matches objects whose property does not equal value.
func Ne(class, prop, value string) Filter { return newProp("ne", class, prop, value) }

// Gt, Ge, Lt and Le compare numerically when both sides are numbers and
// lexically otherwise.
func Gt(class, prop, value string) Filter { return newProp("gt", class, prop, value) }
func Ge(class, prop, value string) Filter { return newProp("ge", class, prop, value) }
func Lt(class, prop, value string) Filter { return newProp("lt", class, prop, value) }
func Le(class, prop, value string) Filter { return newProp("le", class, prop, value) }

// Wcard matches objects whose property matches the regular expression value.
// UCSM evaluates the pattern server side; locally it is matched unanchored.
func Wcard(class, prop, pattern string) (Filter, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("wcard %s.%s: %w", class, prop, err)
	}
	f := newProp("wcard", class, prop, pattern)
	f.re = re
	return f, nil
}

func (f propFilter) element() Element {
	return Element{Name: f.op, Attrs: []xml.Attr{
		{Name: xml.Name{Local: "class"}, Value: f.class},
		{Name: xml.Name{Local: "property"}, Value: f.prop},
		{Name: xml.Name{Local: "value"}, Value: f.value},
	}}
}

func (f propFilter) Match(mo *MO) bool {
	v := mo.Get(f.prop)
	switch f.op {
	case "eq":
		return v == f.value
	case "ne":
		return v != f.value
	case "wcard":
		return f.re.MatchString(v)
	}
	c := compare(v, f.value)
	switch f.op {
	case "gt":
		return c > 0
	case "ge":
		return c >= 0
	case "lt":
		return c < 0
	case "le":
		return c <= 0
	}
	return false
}

func compare(a, b string) int {
	fa, oka := ParseNumber(a)
	fb, okb := ParseNumber(b)
	if oka && okb {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	return strings.Compare(a, b)
}

func (f propFilter) String() string {
	return fmt.Sprintf("%s(%s.%s,%q)", f.op, f.class, f.prop, f.value)
}

type logicFilter struct {
	op   string
	subs []Filter
}

// And matches objects matched by every sub-filter.
func And(f ...Filter) Filter { return logicFilter{op: "and", subs: f} }

// Or matches objects matched by any sub-filter.
func Or(f ...Filter) Filter { return logicFilter{op: "or", subs: f} }

// Not matches objects not matched by f.
func Not(f Filter) Filter { return logicFilter{op: "not", subs: []Filter{f}} }

func (f logicFilter) element() Element {
	el := Element{Name: f.op}
	for _, sub := range f.subs {
		el.Children = append(el.Children, sub.element())
	}
	return el
}

func (f logicFilter) Match(mo *MO) bool {
	switch f.op {
	case "and":
		for _, s := range f.subs {
			if !s.Match(mo) {
				return false
			}
		}
		return true
	case "or":
		for _, s := range f.subs {
			if s.Match(mo) {
				return true
			}
		}
		return false
	case "not":
		return len(f.subs) == 1 && !f.subs[0].Match(mo)
	}
	return false
}

func (f logicFilter) String() string {
	parts := make([]string, len(f.subs))
	for i, s := range f.subs {
		parts[i] = s.String()
	}
	return f.op + "(" + strings.Join(parts, ",") + ")"
}

// inFilter returns <inFilter>f</inFilter>.
func inFilter(f Filter) Element {
	return Element{Name: "inFilter", Children: []Element{f.element()}}
}

// ParseFilterExpr parses a simple command-line filter expression for class,
// of the form "prop OP value" with OP one of = != > >= < <= ~ (~ is a
// regular-expression match), e.g. "severity!=cleared" or "dn~^sys/chassis-1/".
func ParseFilterExpr(class, expr string) (Filter, error) {
	// The operator is the earliest one in the expression (so that values may
	// contain operator characters); at equal positions the longer one wins.
	op, at := "", -1
	for _, o := range []string{"!=", ">=", "<=", "=", ">", "<", "~"} {
		if i := strings.Index(expr, o); i > 0 && (at < 0 || i < at) {
			op, at = o, i
		}
	}
	if at > 0 {
		prop := strings.TrimSpace(expr[:at])
		value := strings.TrimSpace(expr[at+len(op):])
		switch op {
		case "=":
			return Eq(class, prop, value), nil
		case "!=":
			return Ne(class, prop, value), nil
		case ">":
			return Gt(class, prop, value), nil
		case ">=":
			return Ge(class, prop, value), nil
		case "<":
			return Lt(class, prop, value), nil
		case "<=":
			return Le(class, prop, value), nil
		case "~":
			return Wcard(class, prop, value)
		}
	}
	return nil, fmt.Errorf("invalid filter %q: want prop=value, prop!=value, prop>value, prop>=value, prop<value, prop<=value or prop~regexp", expr)
}
