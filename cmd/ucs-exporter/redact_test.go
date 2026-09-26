// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

func TestRedactor(t *testing.T) {
	r := newRedactor([]byte("0123456789abcdef0123456789abcdef"), true)
	hostFc := ucsm.NewMO("adaptorHostFcIf", "sys/chassis-1/blade-1/adaptor-1/host-fc-1",
		"wwn", "20:00:00:25:B5:0A:00:01", "vnicDn", "org-root/org-fin/ls-db01/fc-fc0", "serial", "FCH1234")
	vhba := ucsm.NewMO("vnicFc", "org-root/org-fin/ls-db01/fc-fc0", "addr", "20:00:00:25:b5:0a:00:01", "name", "fc0")
	eth := ucsm.NewMO("vnicEther", "org-root/ls-esx01/ether-eth0", "addr", "00:25:B5:00:00:01", "mtu", "9000")
	top := ucsm.NewMO("topSystem", "sys", "name", "ucs-prod", "systemUpTime", "12:03:04:05", "address", "10.1.2.3",
		"ipv6Addr", "2001:db8:1::10", "mode", "cluster")
	fault := ucsm.NewMO("faultInst", "sys/chassis-1/fault-F1", "descr", "vNIC 00:25:B5:00:00:01 on 10.1.2.3 failed", "severity", "major")
	sp := ucsm.NewMO("lsServer", "org-root/org-fin/ls-db01", "name", "db01", "uuid", "0e1f2a3b-4c5d-6e7f-8091-a2b3c4d5e6f7", "pnDn", "sys/rack-unit-1")
	for _, mo := range []*ucsm.MO{hostFc, vhba, eth, top, fault, sp} {
		r.mo(mo)
	}

	wwn := regexp.MustCompile(`^20:00(:[0-9A-F]{2}){6}$`)
	mac := regexp.MustCompile(`^02(:[0-9A-F]{2}){5}$`)
	if w := hostFc.Get("wwn"); !wwn.MatchString(w) || w != vhba.Get("addr") {
		t.Errorf("WWNs %q, %q: want the same 20:00:.. pseudonym (case-insensitive input)", w, vhba.Get("addr"))
	}
	if m := eth.Get("addr"); !mac.MatchString(m) || !strings.Contains(fault.Get("descr"), m) {
		t.Errorf("MAC %q not consistently pseudonymized (descr %q)", m, fault.Get("descr"))
	}
	if strings.Contains(fault.Get("descr"), "10.1.2.3") || !strings.HasPrefix(top.Get("address"), "198.18.") {
		t.Errorf("IPv4 not redacted: %q, %q", fault.Get("descr"), top.Get("address"))
	}
	if v := top.Get("ipv6Addr"); !strings.HasPrefix(v, "2001:db8::") || v == "2001:db8:1::10" {
		t.Errorf("IPv6 = %q", v)
	}
	if top.Get("systemUpTime") != "12:03:04:05" || top.Get("mode") != "cluster" || eth.Get("mtu") != "9000" {
		t.Errorf("non-identifying values changed: %+v %+v", top.Attrs, eth.Attrs)
	}
	if top.Get("name") == "ucs-prod" || !strings.HasPrefix(hostFc.Get("serial"), "SRL") {
		t.Errorf("site name / serial not redacted: %q %q", top.Get("name"), hostFc.Get("serial"))
	}
	if sp.Get("uuid") == "0e1f2a3b-4c5d-6e7f-8091-a2b3c4d5e6f7" || len(sp.Get("uuid")) != 36 {
		t.Errorf("uuid = %q", sp.Get("uuid"))
	}
	// Names: DNs and DN-valued attributes stay consistent and parseable.
	if !strings.HasPrefix(sp.DN, "org-root/org-org-") || strings.Contains(sp.DN, "db01") || strings.Contains(sp.DN, "fin") {
		t.Errorf("SP DN = %q", sp.DN)
	}
	if vhba.DN != ucsm.ParentDN(vhba.DN)+"/fc-fc0" || ucsm.ParentDN(vhba.DN) != sp.DN || hostFc.Get("vnicDn") != vhba.DN {
		t.Errorf("inconsistent names: sp %q vhba %q vnicDn %q", sp.DN, vhba.DN, hostFc.Get("vnicDn"))
	}
	if sp.Get("pnDn") != "sys/rack-unit-1" {
		t.Errorf("pnDn changed: %q", sp.Get("pnDn"))
	}
}
