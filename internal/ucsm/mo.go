// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import (
	"iter"
	"math"
	"strconv"
	"strings"
)

// Attr is a single managed-object attribute as returned by UCSM.
type Attr struct {
	Name  string
	Value string
}

// MO is a UCSM managed object: its class ID (the XML element name, e.g.
// "computeBlade"), its distinguished name and its attributes. Attribute values
// are always strings on the wire; use Float/Uint to parse numeric values.
//
// The dn attribute is stored in DN and is not repeated in Attrs.
type MO struct {
	Class string
	DN    string
	Attrs []Attr
}

// Lookup returns the value of the named attribute and whether it is present.
func (m *MO) Lookup(name string) (string, bool) {
	if name == "dn" {
		return m.DN, m.DN != ""
	}
	for i := range m.Attrs {
		if m.Attrs[i].Name == name {
			return m.Attrs[i].Value, true
		}
	}
	return "", false
}

// Get returns the value of the named attribute, or "" if it is absent.
func (m *MO) Get(name string) string {
	v, _ := m.Lookup(name)
	return v
}

// Float parses the named attribute as a number. It returns false when the
// attribute is absent, empty, one of the UCSM "no value" sentinels, or not a
// finite number.
func (m *MO) Float(name string) (float64, bool) {
	v, ok := m.Lookup(name)
	if !ok {
		return 0, false
	}
	return ParseNumber(v)
}

// Uint parses the named attribute as an unsigned integer.
func (m *MO) Uint(name string) (uint64, bool) {
	v, ok := m.Lookup(name)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	return n, err == nil
}

// All iterates over the attributes of m, including dn.
func (m *MO) All() iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		if m.DN != "" && !yield("dn", m.DN) {
			return
		}
		for _, a := range m.Attrs {
			if !yield(a.Name, a.Value) {
				return
			}
		}
	}
}

// ParseNumber parses a UCSM numeric attribute value. Values such as
// "not-applicable", "unspecified", "NA" or "" yield false, as do NaN and
// infinities.
func ParseNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	switch strings.ToLower(s) {
	case "not-applicable", "na", "n/a", "unspecified", "unknown", "none", "not-supported", "disabled":
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// NewMO builds an object from alternating attribute names and values. It is
// mainly useful in tests.
func NewMO(class, dn string, kv ...string) *MO {
	mo := &MO{Class: class, DN: dn}
	for i := 0; i+1 < len(kv); i += 2 {
		mo.Attrs = append(mo.Attrs, Attr{Name: kv[i], Value: kv[i+1]})
	}
	return mo
}
