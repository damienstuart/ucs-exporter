// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package modules

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
)

// fcExtended: Fibre Channel error counters and state on the fabric
// interconnects, FCoE uplinks, and the adapter-side vHBA counters.
type fcExtended struct {
	base
	stats []statsClass

	portInfo, portOper, portAdmin, portSpeed, portUp *prometheus.Desc
	pcOper, pcAdmin, pcSpeed, pcUp                   *prometheus.Desc
	memberState, memberUp                            *prometheus.Desc
	fcoeOper, fcoeState, fcoeUp                      map[string]*prometheus.Desc
	vhbaStats                                        []vhbaStats
}

type vhbaStats struct {
	class string
	t     *module.Table
}

var fcoeKinds = []string{kindFCoEUplink, kindFCoEPortChannel}

func newFCExtended() *fcExtended {
	m := &fcExtended{base: base{name: "fc_extended", description: "Fibre Channel error counters, FC/FCoE port and SAN port-channel state, and adapter vHBA FC counters (fcErrStats, fcPIo, fabricFcSanPc, fabricFcoeSanEp, adaptorFcPortStats, adaptorFcIf*Stats)."}}
	m.stats = []statsClass{
		{"fcErrStats", m.kindTables("fcErrStats", fcKinds,
			module.C("crcRx", "crc_errors_total", "Frames received with CRC errors"),
			module.C("discardRx", "receive_discards_total", "Received frames discarded"),
			module.C("discardTx", "transmit_discards_total", "Transmitted frames discarded"),
			module.C("linkFailures", "link_failures_total", "Link failures"),
			module.C("signalLosses", "signal_losses_total", "Loss of signal events"),
			module.C("syncLosses", "sync_losses_total", "Loss of synchronization events"),
			module.C("tooLongRx", "receive_too_long_total", "Frames received that were too long"),
			module.C("tooShortRx", "receive_too_short_total", "Frames received that were too short"),
			module.C("rx", "receive_errors_total", "Receive errors"),
			module.C("tx", "transmit_errors_total", "Transmit errors"),
		)},
		{"etherFcoeInterfaceStats", m.kindTables("etherFcoeInterfaceStats", fcoeKinds,
			module.C("bytesRx", "receive_bytes_total", "FCoE bytes received"),
			module.C("bytesTx", "transmit_bytes_total", "FCoE bytes transmitted"),
			module.C("packetsRx", "receive_packets_total", "FCoE packets received"),
			module.C("packetsTx", "transmit_packets_total", "FCoE packets transmitted"),
			module.C("errorsRx", "receive_errors_total", "FCoE receive errors"),
			module.C("errorsTx", "transmit_errors_total", "FCoE transmit errors"),
			module.C("droppedRx", "receive_dropped_total", "FCoE received packets dropped"),
			module.C("droppedTx", "transmit_dropped_total", "FCoE transmitted packets dropped"),
		)},
	}

	fcp := families[kindFIFCPort].labels
	pc := families[kindFIFCPortChannel].labels
	st := func(ls []string) []string { return append(append([]string{}, ls...), labelState) }
	m.portInfo = m.desc("ucs_fi_fc_port_info", "Fabric interconnect FC port information (UCSM fcPIo).", append(append([]string{}, fcp...), "role", "mode", "wwn", "xcvr_type")...)
	m.portOper = m.desc("ucs_fi_fc_port_oper_state", "Fabric interconnect FC port operational state (UCSM fcPIo.operState).", st(fcp)...)
	m.portAdmin = m.desc("ucs_fi_fc_port_admin_state", "Fabric interconnect FC port administrative state (UCSM fcPIo.adminState).", st(fcp)...)
	m.portSpeed = m.desc("ucs_fi_fc_port_speed_bytes", "Fabric interconnect FC port operational speed in bytes per second (UCSM fcPIo.operSpeed).", fcp...)
	m.portUp = m.desc("ucs_fi_fc_port_up", "Whether the fabric interconnect FC port is up (UCSM fcPIo.operState).", fcp...)
	m.pcOper = m.desc("ucs_fi_fc_port_channel_oper_state", "SAN port channel operational state (UCSM fabricFcSanPc.operState).", st(pc)...)
	m.pcAdmin = m.desc("ucs_fi_fc_port_channel_admin_state", "SAN port channel administrative state (UCSM fabricFcSanPc.adminState).", st(pc)...)
	m.pcSpeed = m.desc("ucs_fi_fc_port_channel_speed_bytes", "SAN port channel aggregate speed in bytes per second (UCSM fabricFcSanPc.operSpeed, in Gbps).", pc...)
	m.pcUp = m.desc("ucs_fi_fc_port_channel_up", "Whether the SAN port channel is up (UCSM fabricFcSanPc.operState).", pc...)
	m.memberState = m.desc("ucs_fi_fc_port_channel_member_state", "SAN port channel member state (UCSM fabricFcSanPcEp.membership).", append(append([]string{}, pc...), labelPort, labelState)...)
	m.memberUp = m.desc("ucs_fi_fc_port_channel_member_up", "Whether the SAN port channel member is up (UCSM fabricFcSanPcEp.membership).", append(append([]string{}, pc...), labelPort)...)
	m.fcoeOper = m.kindDescs(fcoeKinds, "oper_state", "FCoE uplink or port channel operational state (UCSM fabricFcoeSanEp/fabricFcoeSanPc.operState).", labelState)
	m.fcoeState = m.kindDescs(fcoeKinds, "fcoe_state", "FCoE state (UCSM fabricFcoeSanEp/fabricFcoeSanPc.fcoeState).", labelState)
	m.fcoeUp = m.kindDescs(fcoeKinds, "up", "Whether the FCoE uplink or port channel is up (UCSM fabricFcoeSanEp/fabricFcoeSanPc.operState).")

	m.vhbaStats = []vhbaStats{
		{"adaptorFcPortStats", m.table("adaptorFcPortStats", "ucs_vhba_port", vhbaLabels,
			module.C("rxFrames", "receive_frames_total", "Frames received by the vHBA port"),
			module.C("txFrames", "transmit_frames_total", "Frames transmitted by the vHBA port"),
			module.C("rxBadFrames", "receive_bad_frames_total", "Bad frames received by the vHBA port"),
			module.C("txBadFrames", "transmit_bad_frames_total", "Bad frames transmitted by the vHBA port"),
		)},
		{"adaptorFcIfFrameStats", m.table("adaptorFcIfFrameStats", "ucs_vhba", vhbaLabels,
			module.C("rxFrames", "receive_frames_total", "FC frames received by the vHBA"),
			module.C("txFrames", "transmit_frames_total", "FC frames transmitted by the vHBA"),
			module.C("dumpedFrames", "dumped_frames_total", "FC frames dumped by the vHBA"),
			module.C("errorFrames", "error_frames_total", "FC error frames on the vHBA"),
		)},
		{"adaptorFcIfEventStats", m.table("adaptorFcIfEventStats", "ucs_vhba", vhbaLabels,
			module.C("linkFailureCount", "link_failures_total", "vHBA link failures"),
			module.C("lossOfSignalCount", "signal_losses_total", "vHBA loss of signal events"),
			module.C("lossOfSyncCount", "sync_losses_total", "vHBA loss of synchronization events"),
			module.C("invalidCRCCount", "crc_errors_total", "vHBA frames with invalid CRC"),
			module.C("lipCount", "lips_total", "vHBA loop initialization primitives"),
			module.C("nOSCount", "nos_total", "vHBA not-operational sequences"),
			module.C("seqProtocolErrCount", "sequence_protocol_errors_total", "vHBA primitive sequence protocol errors"),
			module.G("secondsSinceLastReset", "seconds_since_last_reset", "Seconds since the vHBA counters were last reset"),
		)},
		{"adaptorFcIfFC4Stats", m.table("adaptorFcIfFC4Stats", "ucs_vhba_fcp", vhbaLabels,
			module.C("inputRequests", "input_requests_total", "FCP input (read) requests"),
			module.C("outputRequests", "output_requests_total", "FCP output (write) requests"),
			module.C("controlRequests", "control_requests_total", "FCP control requests"),
			module.C("inputMegabytes", "input_bytes_total", "FCP bytes read, from megabytes").Scaled(module.MiB),
			module.C("outputMegabytes", "output_bytes_total", "FCP bytes written, from megabytes").Scaled(module.MiB),
		)},
	}

	m.query("fcPIo", "ifRole", "mode", "wwn", "xcvrType", "operState", "adminState", "operSpeed")
	m.query("fabricFcSanPc", "operState", "adminState", "operSpeed")
	m.query("fabricFcSanPcEp", "membership")
	m.query("fabricFcoeSanEp", "operState", "fcoeState")
	m.query("fabricFcoeSanPc", "operState", "fcoeState")
	m.query("adaptorHostFcIf", "vnicDn")
	return m
}

func (m *fcExtended) Collect(s *module.Snapshot, e *module.Emitter) error {
	emitPortStats(s, e, m.stats)

	for _, mo := range s.Class("fcPIo") {
		kind, lv, ok := portEntity(s, mo.DN)
		if !ok || kind != kindFIFCPort {
			e.Drop("unknown_dn")
			continue
		}
		e.Info(m.portInfo, append(append([]string{}, lv...), module.OrUnknown(mo.Get("ifRole")), module.OrUnknown(mo.Get("mode")),
			module.OrUnknown(mo.Get("wwn")), module.OrUnknown(mo.Get("xcvrType")))...)
		e.State(m.portOper, mo.Get("operState"), lv...)
		e.State(m.portAdmin, mo.Get("adminState"), lv...)
		if v, ok := parseSpeed(mo.Get("operSpeed")); ok {
			e.Gauge(m.portSpeed, v, lv...)
		}
		e.Bool(m.portUp, isUp(mo.Get("operState")), lv...)
	}
	for _, mo := range s.Class("fabricFcSanPc") {
		kind, lv, ok := portEntity(s, mo.DN)
		if !ok || kind != kindFIFCPortChannel {
			e.Drop("unknown_dn")
			continue
		}
		e.State(m.pcOper, mo.Get("operState"), lv...)
		e.State(m.pcAdmin, mo.Get("adminState"), lv...)
		if v, ok := parseGbps(mo.Get("operSpeed")); ok {
			e.Gauge(m.pcSpeed, v, lv...)
		}
		e.Bool(m.pcUp, isUp(mo.Get("operState")), lv...)
	}
	for _, mo := range s.Class("fabricFcSanPcEp") {
		d := dn.Parse(mo.DN)
		port, ok := d.FIPort()
		if !ok || d.Cloud != "san" || d.PortChannel == "" {
			e.Drop("unknown_dn")
			continue
		}
		e.State(m.memberState, mo.Get("membership"), d.CloudSide, d.PortChannel, port)
		e.Bool(m.memberUp, mo.Get("membership") == "up", d.CloudSide, d.PortChannel, port)
	}
	for _, class := range []string{"fabricFcoeSanEp", "fabricFcoeSanPc"} {
		for _, mo := range s.Class(class) {
			kind, lv, ok := portEntity(s, mo.DN)
			if !ok || (kind != kindFCoEUplink && kind != kindFCoEPortChannel) {
				e.Drop("unknown_dn")
				continue
			}
			e.State(m.fcoeOper[kind], mo.Get("operState"), lv...)
			e.State(m.fcoeState[kind], mo.Get("fcoeState"), lv...)
			e.Bool(m.fcoeUp[kind], isUp(mo.Get("operState")), lv...)
		}
	}

	for _, vs := range m.vhbaStats {
		hostIfStats(s, e, vs.class, nil, vs.t)
	}
	return nil
}
