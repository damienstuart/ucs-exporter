// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package modules

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// ethernetExtended: detailed Ethernet counters, port and port-channel state,
// chassis/FEX backplane topology and VLAN port usage.
type ethernetExtended struct {
	base
	stats         []statsClass
	uplinkErrors  bool
	adaptorErrors map[string]*module.Table // "rx"/"tx"

	fiInfo, fiOper, fiAdmin, fiSpeed, fiUp *prometheus.Desc
	pcOper, pcAdmin, pcSpeed, pcUp         *prometheus.Desc
	memberState, memberUp                  *prometheus.Desc
	hostOper, hostUp, hostPeer             map[string]*prometheus.Desc
	fabricOper, fabricUp, fabricPeer       map[string]*prometheus.Desc
	vlanPorts                              *module.Table
	vlanAlloc                              *prometheus.Desc
}

func newEthernetExtended(opts config.Options) *ethernetExtended {
	m := &ethernetExtended{
		base:         base{name: "ethernet_extended", description: "Detailed Ethernet counters (packet types, loss, pause, IOM fabric-link errors), port and port-channel state, backplane topology and VLAN port usage."},
		uplinkErrors: opts.AdaptorUplinkErrors,
	}
	detailKinds := []string{kindFIPort, kindFIPortChannel}
	if opts.IOMHostPorts {
		detailKinds = append(detailKinds, kindIOMHostPort, kindFexHostPort)
	}
	m.stats = []statsClass{
		{"etherRxStats", m.kindTables("etherRxStats", detailKinds,
			module.C("unicastPackets", "receive_unicast_packets_total", "Unicast packets received"),
			module.C("multicastPackets", "receive_multicast_packets_total", "Multicast packets received"),
			module.C("broadcastPackets", "receive_broadcast_packets_total", "Broadcast packets received"),
			module.C("jumboPackets", "receive_jumbo_packets_total", "Jumbo packets received"),
		)},
		{"etherTxStats", m.kindTables("etherTxStats", detailKinds,
			module.C("unicastPackets", "transmit_unicast_packets_total", "Unicast packets transmitted"),
			module.C("multicastPackets", "transmit_multicast_packets_total", "Multicast packets transmitted"),
			module.C("broadcastPackets", "transmit_broadcast_packets_total", "Broadcast packets transmitted"),
			module.C("jumboPackets", "transmit_jumbo_packets_total", "Jumbo packets transmitted"),
		)},
		{"etherLossStats", m.kindTables("etherLossStats", detailKinds,
			module.C("SQETest", "sqe_test_errors_total", "SQE test errors"),
			module.C("carrierSense", "carrier_sense_errors_total", "Carrier sense errors"),
			module.C("excessCollision", "excess_collisions_total", "Excessive collisions"),
			module.C("giants", "receive_giants_total", "Oversized (giant) frames received"),
			module.C("lateCollision", "late_collisions_total", "Late collisions"),
			module.C("multiCollision", "multiple_collisions_total", "Multiple collisions"),
			module.C("singleCollision", "single_collisions_total", "Single collisions"),
			module.C("symbol", "symbol_errors_total", "Symbol errors"),
		)},
		{"etherPauseStats", m.kindTables("etherPauseStats", detailKinds,
			module.C("recvPause", "receive_pause_frames_total", "Pause frames received"),
			module.C("xmitPause", "transmit_pause_frames_total", "Pause frames transmitted"),
			module.C("resets", "pause_resets_total", "Pause resets"),
		)},
		{"etherNiErrStats", m.kindTables("etherNiErrStats", []string{kindIOMFabricPort, kindFexFabricPort},
			module.C("crc", "crc_errors_total", "Frames received with CRC errors"),
			module.C("frameTx", "transmit_frame_errors_total", "Transmit frame errors"),
			module.C("inRange", "in_range_length_errors_total", "In-range length errors"),
			module.C("tooLong", "receive_too_long_total", "Frames received that were too long"),
			module.C("tooShort", "receive_too_short_total", "Frames received that were too short"),
		)},
	}

	fip := families[kindFIPort].labels
	pc := families[kindFIPortChannel].labels
	st := func(ls []string) []string { return append(append([]string{}, ls...), labelState) }
	m.fiInfo = m.desc("ucs_fi_port_info", "Fabric interconnect Ethernet port information (UCSM etherPIo).", append(append([]string{}, fip...), "mode", "xcvr_type")...)
	m.fiOper = m.desc("ucs_fi_port_oper_state", "Fabric interconnect Ethernet port operational state (UCSM etherPIo.operState).", st(fip)...)
	m.fiAdmin = m.desc("ucs_fi_port_admin_state", "Fabric interconnect Ethernet port administrative state (UCSM etherPIo.adminState).", st(fip)...)
	m.fiSpeed = m.desc("ucs_fi_port_speed_bytes", "Fabric interconnect Ethernet port operational speed in bytes per second (UCSM etherPIo.operSpeed).", fip...)
	m.fiUp = m.desc("ucs_fi_port_up", "Whether the fabric interconnect Ethernet port is up (UCSM etherPIo.operState).", fip...)
	m.pcOper = m.desc("ucs_fi_port_channel_oper_state", "LAN port channel operational state (UCSM fabricEthLanPc.operState).", st(pc)...)
	m.pcAdmin = m.desc("ucs_fi_port_channel_admin_state", "LAN port channel administrative state (UCSM fabricEthLanPc.adminState).", st(pc)...)
	m.pcSpeed = m.desc("ucs_fi_port_channel_speed_bytes", "LAN port channel aggregate bandwidth in bytes per second (UCSM fabricEthLanPc.bandwidth, in Gbps).", pc...)
	m.pcUp = m.desc("ucs_fi_port_channel_up", "Whether the LAN port channel is up (UCSM fabricEthLanPc.operState).", pc...)
	m.memberState = m.desc("ucs_fi_port_channel_member_state", "LAN port channel member state (UCSM fabricEthLanPcEp.membership).", append(append([]string{}, pc...), labelPort, labelState)...)
	m.memberUp = m.desc("ucs_fi_port_channel_member_up", "Whether the LAN port channel member is up (UCSM fabricEthLanPcEp.membership).", append(append([]string{}, pc...), labelPort)...)

	hostKinds := []string{kindIOMHostPort, kindFexHostPort}
	fabricKinds := []string{kindIOMFabricPort, kindFexFabricPort}
	m.hostOper = m.kindDescs(hostKinds, "oper_state", "IOM/FEX host port operational state (UCSM etherServerIntFIo.operState).", labelState)
	m.hostUp = m.kindDescs(hostKinds, "up", "Whether the IOM/FEX host port is up (UCSM etherServerIntFIo.operState).")
	m.hostPeer = m.kindDescs(hostKinds, "peer_info", "Server adapter port connected to an IOM/FEX host port (UCSM etherServerIntFIo.peerDn).", labelServer, labelAdaptor, labelInterface)
	m.fabricOper = m.kindDescs(fabricKinds, "oper_state", "IOM/FEX fabric port operational state (UCSM etherSwitchIntFIo.operState).", labelState)
	m.fabricUp = m.kindDescs(fabricKinds, "up", "Whether the IOM/FEX fabric port is up (UCSM etherSwitchIntFIo.operState).")
	m.fabricPeer = m.kindDescs(fabricKinds, "peer_info", "Fabric interconnect port connected to an IOM/FEX fabric port (UCSM etherSwitchIntFIo.peerDn).", "peer_fabric", "peer_port")

	m.vlanPorts = m.table("swVlanPortNs", "ucs_fi", []string{labelFabric},
		module.G("totalVlanPortCount", "vlan_ports", "VLAN port instances in use on the fabric interconnect"),
		module.G("accessVlanPortCount", "vlan_ports_access", "Access VLAN port instances in use"),
		module.G("borderVlanPortCount", "vlan_ports_border", "Border (uplink) VLAN port instances in use"),
		module.G("limit", "vlan_ports_limit", "Maximum VLAN port instances supported"),
	)
	m.vlanAlloc = m.desc("ucs_fi_vlan_ports_alloc_state", "VLAN port instance allocation status (UCSM swVlanPortNs.allocStatus).", labelFabric, labelState)
	m.query("swVlanPortNs", "allocStatus")

	if m.uplinkErrors {
		m.adaptorErrors = map[string]*module.Table{}
		for dir, name := range map[string]string{"rx": "receive", "tx": "transmit"} {
			m.adaptorErrors[dir] = m.table("adaptorEthPortErrStats", "ucs_adaptor_uplink_"+name, []string{labelServer, labelAdaptor, labelInterface},
				module.C("badCrcPackets", "bad_crc_packets_total", "Packets with bad CRC ("+name+")"),
				module.C("badLengthPackets", "bad_length_packets_total", "Packets with bad length ("+name+")"),
				module.C("macDiscardedPackets", "mac_discarded_packets_total", "Packets discarded by the MAC ("+name+")"),
				module.C("noBufferDropPackets", "no_buffer_drop_packets_total", "Packets dropped for lack of buffers ("+name+")"),
			)
		}
		m.query("adaptorEthPortErrStats", "trafficDirection")
	}

	m.query("etherPIo", "ifRole", "mode", "xcvrType", "operState", "adminState", "operSpeed")
	m.query("fabricEthLanPc", "operState", "adminState", "bandwidth")
	m.query("fabricEthLanPcEp", "membership")
	m.query("etherServerIntFIo", "operState", "peerDn")
	m.query("etherSwitchIntFIo", "operState", "peerDn")
	return m
}

func (m *ethernetExtended) Collect(s *module.Snapshot, e *module.Emitter) error {
	emitPortStats(s, e, m.stats)

	for _, mo := range s.Class("etherPIo") {
		kind, lv, ok := portEntity(s, mo.DN)
		if !ok || kind != kindFIPort {
			e.Drop("unknown_dn")
			continue
		}
		e.Info(m.fiInfo, append(append([]string{}, lv...), module.OrUnknown(mo.Get("mode")), module.OrUnknown(mo.Get("xcvrType")))...)
		e.State(m.fiOper, mo.Get("operState"), lv...)
		e.State(m.fiAdmin, mo.Get("adminState"), lv...)
		if v, ok := parseSpeed(mo.Get("operSpeed")); ok {
			e.Gauge(m.fiSpeed, v, lv...)
		}
		e.Bool(m.fiUp, isUp(mo.Get("operState")), lv...)
	}
	for _, mo := range s.Class("fabricEthLanPc") {
		kind, lv, ok := portEntity(s, mo.DN)
		if !ok || kind != kindFIPortChannel {
			e.Drop("unknown_dn")
			continue
		}
		e.State(m.pcOper, mo.Get("operState"), lv...)
		e.State(m.pcAdmin, mo.Get("adminState"), lv...)
		// operSpeed is the speed of each member; bandwidth is the total.
		if v, ok := parseGbps(mo.Get("bandwidth")); ok {
			e.Gauge(m.pcSpeed, v, lv...)
		}
		e.Bool(m.pcUp, isUp(mo.Get("operState")), lv...)
	}
	for _, mo := range s.Class("fabricEthLanPcEp") {
		d := dn.Parse(mo.DN)
		port, ok := d.FIPort()
		if !ok || d.Cloud != "lan" || d.PortChannel == "" {
			e.Drop("unknown_dn")
			continue
		}
		e.State(m.memberState, mo.Get("membership"), d.CloudSide, d.PortChannel, port)
		e.Bool(m.memberUp, mo.Get("membership") == "up", d.CloudSide, d.PortChannel, port)
	}

	for _, mo := range s.Class("etherServerIntFIo") {
		kind, lv, ok := portEntity(s, mo.DN)
		if !ok || (kind != kindIOMHostPort && kind != kindFexHostPort) {
			e.Drop("unknown_dn")
			continue
		}
		e.State(m.hostOper[kind], mo.Get("operState"), lv...)
		e.Bool(m.hostUp[kind], isUp(mo.Get("operState")), lv...)
		peer := dn.Parse(mo.Get("peerDn"))
		if server, adaptor, iface, ok := hostInterface(&peer); ok {
			e.Info(m.hostPeer[kind], append(append([]string{}, lv...), server, adaptor, iface)...)
		}
	}
	for _, mo := range s.Class("etherSwitchIntFIo") {
		kind, lv, ok := portEntity(s, mo.DN)
		if !ok || (kind != kindIOMFabricPort && kind != kindFexFabricPort) {
			e.Drop("unknown_dn")
			continue
		}
		e.State(m.fabricOper[kind], mo.Get("operState"), lv...)
		e.Bool(m.fabricUp[kind], isUp(mo.Get("operState")), lv...)
		peer := dn.Parse(mo.Get("peerDn"))
		if port, ok := peer.FIPort(); ok && peer.Switch != "" {
			e.Info(m.fabricPeer[kind], append(append([]string{}, lv...), peer.Switch, port)...)
		}
	}

	for _, mo := range s.Class("swVlanPortNs") {
		d := dn.Parse(mo.DN)
		if d.Switch == "" {
			e.Drop("unknown_dn")
			continue
		}
		m.vlanPorts.Emit(e, mo, d.Switch)
		if v := mo.Get("allocStatus"); v != "" {
			e.State(m.vlanAlloc, v, d.Switch)
		}
	}

	if m.uplinkErrors {
		for _, mo := range s.Class("adaptorEthPortErrStats") {
			parent := s.Parent(mo)
			if parent == nil || parent.Class != "adaptorExtEthIf" {
				continue // host interface counters duplicate adaptorVnicStats
			}
			dir := mo.Get("trafficDirection")
			if dir == "" {
				dir = strings.TrimPrefix(ucsm.LastRN(mo.DN), "eth-port-err-stats-")
			}
			t, ok := m.adaptorErrors[dir]
			d := dn.Parse(parent.DN)
			server, adaptor, iface, ok2 := hostInterface(&d)
			if !ok || !ok2 {
				e.Drop("unknown_dn")
				continue
			}
			t.Emit(e, mo, server, adaptor, iface)
		}
	}
	return nil
}
