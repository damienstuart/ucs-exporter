// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import "strings"

// SplitDN splits a distinguished name into its relative names. UCSM wraps
// naming values that contain special characters in square brackets, e.g.
// "org-root/ls-[web/01]", so '/' is only treated as a separator outside
// brackets.
func SplitDN(dn string) []string {
	if dn == "" {
		return nil
	}
	var (
		rns   []string
		depth int
		start int
	)
	for i := 0; i < len(dn); i++ {
		switch dn[i] {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case '/':
			if depth == 0 {
				rns = append(rns, dn[start:i])
				start = i + 1
			}
		}
	}
	return append(rns, dn[start:])
}

// ParentDN returns the DN of the parent object, or "" for a top-level object.
func ParentDN(dn string) string {
	if i := lastSep(dn); i >= 0 {
		return dn[:i]
	}
	return ""
}

// LastRN returns the relative name of the object itself.
func LastRN(dn string) string {
	return dn[lastSep(dn)+1:]
}

// lastSep returns the index of the last '/' outside brackets, or -1.
func lastSep(dn string) int {
	depth := 0
	for i := len(dn) - 1; i >= 0; i-- {
		switch dn[i] {
		case ']':
			depth++
		case '[':
			if depth > 0 {
				depth--
			}
		case '/':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// RNValue returns the naming value of rn if it starts with prefix, stripping
// one level of square brackets: RNValue("ls-[a/b]", "ls-") returns "a/b".
func RNValue(rn, prefix string) (string, bool) {
	v, ok := strings.CutPrefix(rn, prefix)
	if !ok {
		return "", false
	}
	return Unbracket(v), true
}

// Unbracket removes one enclosing pair of square brackets from v, if present.
func Unbracket(v string) string {
	if len(v) >= 2 && v[0] == '[' && v[len(v)-1] == ']' {
		return v[1 : len(v)-1]
	}
	return v
}
