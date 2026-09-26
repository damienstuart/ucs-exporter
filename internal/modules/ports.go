// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Derived from ucs/ethernet.py, ucs/fibrechannel.py and ucs/vnic.py of
// prometheus-ucs-exporter, (c) 2022 Marshall Wace, GPL-3.0-only.

package modules

import (
	"slices"

	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// statsClass is a statistics class exported for several port kinds.
type statsClass struct {
	class  string
	tables map[string]*module.Table
}

// emitPortStats emits the tables of each statistics class for every object
// whose parent is a port of one of the tables' kinds.
func emitPortStats(s *module.Snapshot, e *module.Emitter, classes []statsClass) {
	for _, sc := range classes {
		for _, mo := range s.Class(sc.class) {
			kind, lv, ok := portEntity(s, ucsm.ParentDN(mo.DN))
			if !ok {
				e.Drop("unknown_dn")
				continue
			}
			if t, ok := sc.tables[kind]; ok {
				t.Emit(e, mo, lv...)
			}
		}
	}
}

// ethernet: traffic and error counters of FI ports, LAN port channels and
// IOM/FEX host ports.
type ethernet struct {
	base
	stats []statsClass
}

var ethernetKinds = []string{kindFIPort, kindFIPortChannel, kindIOMHostPort, kindFexHostPort}

func newEthernet() *ethernet {
	m := &ethernet{base: base{name: "ethernet", description: "Ethernet traffic and error counters for FI ports, LAN port channels and IOM/FEX host ports (etherRxStats, etherTxStats, etherErrStats)."}}
	m.stats = []statsClass{
		{"etherRxStats", m.kindTables("etherRxStats", ethernetKinds,
			module.C("totalBytes", "receive_bytes_total", "Bytes received"),
			module.C("totalPackets", "receive_packets_total", "Packets received"),
		)},
		{"etherTxStats", m.kindTables("etherTxStats", ethernetKinds,
			module.C("totalBytes", "transmit_bytes_total", "Bytes transmitted"),
			module.C("totalPackets", "transmit_packets_total", "Packets transmitted"),
		)},
		{"etherErrStats", m.kindTables("etherErrStats", ethernetKinds,
			module.C("align", "align_errors_total", "Alignment errors"),
			module.C("fcs", "fcs_errors_total", "Frame check sequence errors"),
			module.C("rcv", "receive_errors_total", "Receive errors"),
			module.C("underSize", "receive_undersize_total", "Undersized frames received"),
			module.C("intMacRx", "receive_internal_mac_errors_total", "Internal MAC receive errors"),
			module.C("xmit", "transmit_errors_total", "Transmit errors"),
			module.C("deferredTx", "transmit_deferred_total", "Deferred transmissions"),
			module.C("intMacTx", "transmit_internal_mac_errors_total", "Internal MAC transmit errors"),
			module.C("outDiscard", "transmit_discards_total", "Frames discarded on output"),
		)},
	}
	m.query("etherPIo", "ifRole")
	return m
}

func (m *ethernet) Collect(s *module.Snapshot, e *module.Emitter) error {
	emitPortStats(s, e, m.stats)
	return nil
}

// fc: Fibre Channel traffic of FI FC ports and SAN port channels.
type fc struct {
	base
	stats []statsClass
}

var fcKinds = []string{kindFIFCPort, kindFIFCPortChannel}

func newFC() *fc {
	m := &fc{base: base{name: "fc", description: "Fibre Channel traffic for FI FC ports and SAN port channels (fcStats)."}}
	m.stats = []statsClass{{"fcStats", m.kindTables("fcStats", fcKinds,
		module.C("bytesRx", "receive_bytes_total", "Fibre Channel bytes received"),
		module.C("bytesTx", "transmit_bytes_total", "Fibre Channel bytes transmitted"),
		module.C("packetsRx", "receive_frames_total", "Fibre Channel frames received"),
		module.C("packetsTx", "transmit_frames_total", "Fibre Channel frames transmitted"),
	)}}
	return m
}

func (m *fc) Collect(s *module.Snapshot, e *module.Emitter) error {
	emitPortStats(s, e, m.stats)
	return nil
}

// Adaptor host interface label sets: the physical interface plus the
// service profile and vNIC/vHBA it is bound to.
var (
	vnicLabels = []string{labelServer, labelAdaptor, labelInterface, labelOrg, labelServiceProfile, labelVnic}
	vhbaLabels = []string{labelServer, labelAdaptor, labelInterface, labelOrg, labelServiceProfile, labelVhba}
)

// hostIfLabels returns the label values for an adaptor host interface
// object (adaptorHostEthIf or adaptorHostFcIf) and whether it is a vHBA.
// Interfaces not bound to a service profile are skipped.
func hostIfLabels(hostIf *ucsm.MO) (lv []string, fc bool, ok bool) {
	d := dn.Parse(hostIf.DN)
	server, adaptor, iface, ok := hostInterface(&d)
	if !ok || (d.HostEth == "" && d.HostFc == "") {
		return nil, false, false
	}
	org, sp, name, ok := vnicOwner(hostIf.Get("vnicDn"))
	if !ok {
		return nil, false, false
	}
	return []string{server, adaptor, iface, org, sp, name}, d.HostFc != "", true
}

// hostIfStats emits statistics objects whose parent is an adaptor host
// interface, using the vNIC or vHBA table.
func hostIfStats(s *module.Snapshot, e *module.Emitter, class string, eth, fcTable *module.Table) {
	for _, mo := range s.Class(class) {
		parent := s.Parent(mo)
		if parent == nil || (parent.Class != "adaptorHostEthIf" && parent.Class != "adaptorHostFcIf") {
			e.Drop("no_host_interface")
			continue
		}
		lv, isFC, ok := hostIfLabels(parent)
		if !ok {
			continue // not bound to a service profile (e.g. unassociated server)
		}
		t := eth
		if isFC {
			t = fcTable
		}
		if t != nil {
			t.Emit(e, mo, lv...)
		}
	}
}

// vnic: per-vNIC and per-vHBA traffic counters from the adapters.
type vnic struct {
	base
	eth, fc *module.Table
}

func newVnic() *vnic {
	m := &vnic{base: base{name: "vnic", description: "Traffic counters for each vNIC and vHBA, labelled with its service profile (adaptorVnicStats)."}}
	rows := func(what string) []module.Row {
		return []module.Row{
			module.C("bytesRx", "receive_bytes_total", "Bytes received by the "+what),
			module.C("bytesTx", "transmit_bytes_total", "Bytes transmitted by the "+what),
			module.C("packetsRx", "receive_packets_total", "Packets received by the "+what),
			module.C("packetsTx", "transmit_packets_total", "Packets transmitted by the "+what),
			module.C("errorsRx", "receive_errors_total", "Receive errors of the "+what),
			module.C("errorsTx", "transmit_errors_total", "Transmit errors of the "+what),
			module.C("droppedRx", "receive_dropped_total", "Received packets dropped by the "+what),
			module.C("droppedTx", "transmit_dropped_total", "Transmitted packets dropped by the "+what),
		}
	}
	m.eth = m.table("adaptorVnicStats", "ucs_vnic", vnicLabels, rows("vNIC")...)
	m.fc = m.table("adaptorVnicStats", "ucs_vhba", vhbaLabels, rows("vHBA")...)
	m.query("adaptorHostEthIf", "vnicDn")
	m.query("adaptorHostFcIf", "vnicDn")
	return m
}

func (m *vnic) Collect(s *module.Snapshot, e *module.Emitter) error {
	hostIfStats(s, e, "adaptorVnicStats", m.eth, m.fc)
	return nil
}

// kindsWith returns kinds plus extra.
func kindsWith(kinds []string, extra ...string) []string {
	return slices.Concat(kinds, extra)
}
