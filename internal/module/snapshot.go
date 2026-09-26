// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package module

import (
	"sync"
	"time"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// ClassData is the result of querying one class.
type ClassData struct {
	Objects []*ucsm.MO
	// FetchedAt is when the objects were retrieved. For stale data carried
	// forward from an earlier poll it is the time of that poll.
	FetchedAt time.Time
	Stale     bool
}

// Snapshot is the read-only set of objects from one poll of a domain, with
// lazily built indexes for cross-object lookups.
type Snapshot struct {
	domain  string
	classes map[string]*ClassData
	byDN    map[string]*ucsm.MO

	mu       sync.Mutex
	children map[string][]*ucsm.MO
	indexes  map[string]map[string][]*ucsm.MO
}

// NewSnapshot builds a snapshot from per-class results.
func NewSnapshot(domain string, classes map[string]*ClassData) *Snapshot {
	n := 0
	for _, cd := range classes {
		n += len(cd.Objects)
	}
	s := &Snapshot{domain: domain, classes: classes, byDN: make(map[string]*ucsm.MO, n)}
	for _, cd := range classes {
		for _, mo := range cd.Objects {
			s.byDN[mo.DN] = mo
		}
	}
	return s
}

// Domain returns the domain name.
func (s *Snapshot) Domain() string { return s.domain }

// Has reports whether the class was retrieved (possibly stale).
func (s *Snapshot) Has(class string) bool {
	_, ok := s.classes[class]
	return ok
}

// Data returns the per-class result, or nil.
func (s *Snapshot) Data(class string) *ClassData { return s.classes[class] }

// Class returns all objects of a class.
func (s *Snapshot) Class(class string) []*ucsm.MO {
	if cd, ok := s.classes[class]; ok {
		return cd.Objects
	}
	return nil
}

// Get returns the object with the given DN, or nil.
func (s *Snapshot) Get(dn string) *ucsm.MO { return s.byDN[dn] }

// Parent returns the parent object of mo, or nil if it was not retrieved.
func (s *Snapshot) Parent(mo *ucsm.MO) *ucsm.MO { return s.byDN[ucsm.ParentDN(mo.DN)] }

// Ancestor returns the nearest retrieved ancestor of dn (or dn itself) of the
// given class, or nil.
func (s *Snapshot) Ancestor(dn, class string) *ucsm.MO {
	for d := dn; d != ""; d = ucsm.ParentDN(d) {
		if mo := s.byDN[d]; mo != nil && mo.Class == class {
			return mo
		}
	}
	return nil
}

// Children returns the retrieved objects whose parent is dn.
func (s *Snapshot) Children(dn string) []*ucsm.MO {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.children == nil {
		s.children = make(map[string][]*ucsm.MO, len(s.byDN))
		for d, mo := range s.byDN {
			p := ucsm.ParentDN(d)
			s.children[p] = append(s.children[p], mo)
		}
	}
	return s.children[dn]
}

// ChildrenOfClass returns the children of dn with the given class.
func (s *Snapshot) ChildrenOfClass(dn, class string) []*ucsm.MO {
	var out []*ucsm.MO
	for _, mo := range s.Children(dn) {
		if mo.Class == class {
			out = append(out, mo)
		}
	}
	return out
}

// IndexBy returns the objects of class grouped by the value of attr.
func (s *Snapshot) IndexBy(class, attr string) map[string][]*ucsm.MO {
	key := class + "\x00" + attr
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.indexes[key]; ok {
		return idx
	}
	idx := map[string][]*ucsm.MO{}
	for _, mo := range s.Class(class) {
		v := mo.Get(attr)
		idx[v] = append(idx[v], mo)
	}
	if s.indexes == nil {
		s.indexes = map[string]map[string][]*ucsm.MO{}
	}
	s.indexes[key] = idx
	return idx
}
