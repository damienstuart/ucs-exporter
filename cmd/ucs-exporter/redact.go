// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// redactor pseudonymizes identifying values in captured objects. The same
// input always maps to the same output within a capture (HMAC with a random
// or persisted key), so cross-object references such as a WWN appearing in
// several classes stay consistent. Formats are preserved so that parsing
// code sees realistic values.
type redactor struct {
	key   []byte
	names bool
	cache map[string]string
}

func newRedactor(key []byte, names bool) *redactor {
	return &redactor{key: key, names: names, cache: map[string]string{}}
}

// redactKey returns the pseudonymization key: read from file if it exists,
// otherwise random (and saved to file if one is given).
func redactKey(file string) ([]byte, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err == nil {
			key, err := hex.DecodeString(strings.TrimSpace(string(b)))
			if err != nil || len(key) < 16 {
				return nil, fmt.Errorf("%s: invalid key", file)
			}
			return key, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if file != "" {
		if err := os.WriteFile(file, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
			return nil, err
		}
	}
	return key, nil
}

var (
	// A MAC (6 octets) or WWN (8 octets); the longer form wins.
	hwAddrRE = regexp.MustCompile(`\b[0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5}(?:(?::[0-9A-Fa-f]{2}){2})?\b`)
	ipv4RE   = regexp.MustCompile(`\b(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}\b`)
	uuidRE   = regexp.MustCompile(`\b[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\b`)
	// IPv6 is only looked for in address attributes: the pattern also
	// matches uptimes such as "12:03:04:05".
	ipv6RE   = regexp.MustCompile(`(?i)^[0-9a-f:]*::?[0-9a-f:]*$`)
	ipAttrRE = regexp.MustCompile(`(?i)(ip|addr|gw)`)
)

// Attributes whose whole value is an identifier.
var serialAttrs = map[string]bool{
	"serial": true, "assetTag": true, "usrLbl": true,
}

// Attributes of topSystem that identify the site.
var siteAttrs = map[string]bool{"name": true, "owner": true, "site": true, "descr": true}

func (r *redactor) hash(kind, v string) []byte {
	m := hmac.New(sha256.New, r.key)
	m.Write([]byte(kind + "\x00" + v))
	return m.Sum(nil)
}

func (r *redactor) memo(kind, v string, f func([]byte) string) string {
	k := kind + "\x00" + v
	if out, ok := r.cache[k]; ok {
		return out
	}
	out := f(r.hash(kind, v))
	r.cache[k] = out
	return out
}

func colonHex(b []byte, sep string) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, sep)
}

func (r *redactor) mac(v string) string {
	return r.memo("mac", strings.ToUpper(v), func(h []byte) string { return "02:" + colonHex(h[:5], ":") })
}

func (r *redactor) wwn(v string) string {
	return r.memo("wwn", strings.ToUpper(v), func(h []byte) string { return "20:00:" + colonHex(h[:6], ":") })
}

func (r *redactor) ipv4(v string) string {
	return r.memo("ip", v, func(h []byte) string { return fmt.Sprintf("198.18.%d.%d", h[0], h[1]) })
}

func (r *redactor) ipv6(v string) string {
	return r.memo("ip6", v, func(h []byte) string { return fmt.Sprintf("2001:db8::%x:%x", h[0:2], h[2:4]) })
}

func (r *redactor) uuid(v string) string {
	return r.memo("uuid", strings.ToLower(v), func(h []byte) string {
		x := hex.EncodeToString(h[:16])
		return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
	})
}

func (r *redactor) serial(v string) string {
	return r.memo("serial", v, func(h []byte) string { return "SRL" + strings.ToUpper(hex.EncodeToString(h[:4])) })
}

func (r *redactor) name(kind, v string) string {
	return r.memo("name-"+kind, v, func(h []byte) string { return kind + "-" + hex.EncodeToString(h[:3]) })
}

// text replaces identifiers embedded anywhere in the value of attribute
// attr.
func (r *redactor) text(attr, v string) string {
	v = hwAddrRE.ReplaceAllStringFunc(v, func(s string) string {
		if strings.Count(s, ":") == 7 {
			return r.wwn(s)
		}
		return r.mac(s)
	})
	v = uuidRE.ReplaceAllStringFunc(v, r.uuid)
	v = ipv4RE.ReplaceAllStringFunc(v, r.ipv4)
	if ipAttrRE.MatchString(attr) && strings.Count(v, ":") >= 2 && ipv6RE.MatchString(v) && !hwAddrRE.MatchString(v) {
		v = r.ipv6(v)
	}
	return v
}

// dn pseudonymizes organization and service profile names in a DN.
func (r *redactor) dn(v string) string {
	if !r.names || !strings.HasPrefix(v, "org-") {
		return v
	}
	rns := ucsm.SplitDN(v)
	for i, rn := range rns {
		switch {
		case rn == "org-root":
		case strings.HasPrefix(rn, "org-"):
			rns[i] = "org-" + r.name("org", ucsm.Unbracket(rn[4:]))
		case strings.HasPrefix(rn, "ls-"):
			rns[i] = "ls-" + r.name("sp", ucsm.Unbracket(rn[3:]))
		}
	}
	return strings.Join(rns, "/")
}

func (r *redactor) mo(mo *ucsm.MO) {
	mo.DN = r.dn(mo.DN)
	for i := range mo.Attrs {
		a := &mo.Attrs[i]
		v := a.Value
		if v == "" || v == "derived" || v == "unspecified" || v == "not-applicable" || v == "N/A" || v == "0" {
			continue
		}
		switch {
		case serialAttrs[a.Name]:
			a.Value = r.serial(v)
		case mo.Class == "topSystem" && siteAttrs[a.Name]:
			a.Value = r.serial(v)
		case r.names && mo.Class == "lsServer" && a.Name == "name":
			a.Value = r.name("sp", v)
		case strings.HasPrefix(v, "org-"):
			a.Value = r.dn(v)
		default:
			a.Value = r.text(a.Name, v)
		}
	}
}
