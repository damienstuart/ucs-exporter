// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package modules

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// virtual: service profiles, their vNICs and vHBAs, the adapter host
// interfaces they are bound to, virtual circuits and adapters.
type virtual struct {
	base
	vlans string

	spAssoc, spOper, spConfig, spOK, spTemplate            *prometheus.Desc
	vnicInfo, vnicMTU, vnicVlans, vnicVlansDown, vnicVlan  *prometheus.Desc
	vhbaInfo                                               *prometheus.Desc
	vnicLink, vnicOper, vnicUp, vhbaLink, vhbaOper, vhbaUp *prometheus.Desc
	vifOper, vifLink, vifUp, vifPort, vifPC                *prometheus.Desc
	adpInfo, adpOperability, adpThermal, adpOK             *prometheus.Desc
	upInfo, upLink, upOper, upUp                           *prometheus.Desc
}

func newVirtual(opts config.Options) *virtual {
	m := &virtual{
		base:  base{name: "virtual", description: "Service profiles, vNICs, vHBAs, virtual circuits (VIFs) and adapters (lsServer, vnicEther, vnicFc, dcxVc, adaptorHost*If, adaptorUnit, adaptorExtEthIf)."},
		vlans: opts.VirtualVLANs,
	}
	sp := []string{labelOrg, labelServiceProfile}
	lvnic := []string{labelOrg, labelServiceProfile, labelVnic}
	lvhba := []string{labelOrg, labelServiceProfile, labelVhba}
	vif := []string{labelServer, labelFabric, "vif", labelOrg, labelServiceProfile, labelVnic, "transport"}
	adp := []string{labelServer, labelAdaptor}
	adpi := []string{labelServer, labelAdaptor, labelInterface}
	st := func(ls []string) []string { return append(append([]string{}, ls...), labelState) }

	m.spAssoc = m.desc("ucs_service_profile_assoc_state", "Service profile association state (UCSM lsServer.assocState).", st(sp)...)
	m.spOper = m.desc("ucs_service_profile_oper_state", "Service profile operational state (UCSM lsServer.operState).", st(sp)...)
	m.spConfig = m.desc("ucs_service_profile_config_state", "Service profile configuration state (UCSM lsServer.configState).", st(sp)...)
	m.spOK = m.desc("ucs_service_profile_ok", "Whether the service profile operational state is ok (UCSM lsServer.operState).", sp...)
	m.spTemplate = m.desc("ucs_service_profile_template_info", "Template a service profile was instantiated from (UCSM lsServer.operSrcTemplName).", append(append([]string{}, sp...), "template", "template_org")...)

	m.vnicInfo = m.desc("ucs_vnic_info", "vNIC configuration: MAC address and configured fabric (UCSM vnicEther).", append(append([]string{}, lvnic...), "mac", "admin_fabric")...)
	m.vnicMTU = m.desc("ucs_vnic_mtu_bytes", "vNIC MTU in bytes (UCSM vnicEther.mtu).", lvnic...)
	m.vnicVlans = m.desc("ucs_vnic_vlans", "VLANs configured on a vNIC (UCSM vnicEtherIf).", lvnic...)
	m.vnicVlansDown = m.desc("ucs_vnic_vlans_down", "VLANs configured on a vNIC whose operational state is not up (UCSM vnicEtherIf.operState).", lvnic...)
	m.vnicVlan = m.desc("ucs_vnic_vlan_info", "VLAN membership of a vNIC (UCSM vnicEtherIf).", append(append([]string{}, lvnic...), "vlan", "vlan_name", "native")...)
	m.vhbaInfo = m.desc("ucs_vhba_info", "vHBA configuration: WWPN, WWNN, configured fabric and VSAN (UCSM vnicFc, vnicFcNode, vnicFcIf).", append(append([]string{}, lvhba...), "wwpn", "wwnn", "admin_fabric", "vsan", "vsan_name")...)

	m.vnicLink = m.desc("ucs_vnic_link_state", "Link state of the adapter interface backing a vNIC (UCSM adaptorHostEthIf.linkState).", st(vnicLabels)...)
	m.vnicOper = m.desc("ucs_vnic_oper_state", "Operational state of the adapter interface backing a vNIC (UCSM adaptorHostEthIf.operState).", st(vnicLabels)...)
	m.vnicUp = m.desc("ucs_vnic_up", "Whether the link of the adapter interface backing a vNIC is up (UCSM adaptorHostEthIf.linkState).", vnicLabels...)
	m.vhbaLink = m.desc("ucs_vhba_link_state", "Link state of the adapter interface backing a vHBA (UCSM adaptorHostFcIf.linkState).", st(vhbaLabels)...)
	m.vhbaOper = m.desc("ucs_vhba_oper_state", "Operational state of the adapter interface backing a vHBA (UCSM adaptorHostFcIf.operState).", st(vhbaLabels)...)
	m.vhbaUp = m.desc("ucs_vhba_up", "Whether the link of the adapter interface backing a vHBA is up (UCSM adaptorHostFcIf.linkState).", vhbaLabels...)

	m.vifOper = m.desc("ucs_vif_oper_state", "Virtual circuit (VIF) operational state; the vnic label holds the vNIC or vHBA name (UCSM dcxVc.operState).", st(vif)...)
	m.vifLink = m.desc("ucs_vif_link_state", "Virtual circuit (VIF) link state (UCSM dcxVc.linkState).", st(vif)...)
	m.vifUp = m.desc("ucs_vif_up", "Whether the virtual circuit (VIF) link is up (UCSM dcxVc.linkState).", vif...)
	m.vifPort = m.desc("ucs_vif_pinned_port_info", "Fabric interconnect uplink port a virtual circuit is pinned to (UCSM dcxVc.operBorderSlotId/operBorderAggrPortId/operBorderPortId).", append(append([]string{}, vif...), labelPort)...)
	m.vifPC = m.desc("ucs_vif_pinned_port_channel_info", "Fabric interconnect uplink port channel a virtual circuit is pinned to (UCSM dcxVc.operBorderPortId with operBorderSlotId 0).", append(append([]string{}, vif...), labelPortChannel)...)

	m.adpInfo = m.desc("ucs_adaptor_info", "Adapter information (UCSM adaptorUnit).", append(append([]string{}, adp...), "model", "serial", "vendor")...)
	m.adpOperability = m.desc("ucs_adaptor_operability_state", "Adapter operability (UCSM adaptorUnit.operability).", st(adp)...)
	m.adpThermal = m.desc("ucs_adaptor_thermal_state", "Adapter thermal state (UCSM adaptorUnit.thermal).", st(adp)...)
	m.adpOK = m.desc("ucs_adaptor_ok", "Whether the adapter is operable (UCSM adaptorUnit.operability).", adp...)
	m.upInfo = m.desc("ucs_adaptor_uplink_info", "Adapter uplink (adapter to IOM/FEX/FI link) and the fabric it connects to (UCSM adaptorExtEthIf.switchId).", append(append([]string{}, adpi...), labelFabric)...)
	m.upLink = m.desc("ucs_adaptor_uplink_link_state", "Adapter uplink link state (UCSM adaptorExtEthIf.linkState).", st(adpi)...)
	m.upOper = m.desc("ucs_adaptor_uplink_oper_state", "Adapter uplink operational state (UCSM adaptorExtEthIf.operState).", st(adpi)...)
	m.upUp = m.desc("ucs_adaptor_uplink_up", "Whether the adapter uplink is up (UCSM adaptorExtEthIf.operState).", adpi...)

	m.query("lsServer", "type", "assocState", "operState", "configState", "pnDn", "operSrcTemplName")
	m.query("vnicEther", "addr", "switchId", "mtu")
	if m.vlans != "off" {
		m.query("vnicEtherIf", "vnet", "name", "defaultNet", "operState")
	}
	m.query("vnicFc", "addr", "switchId")
	m.query("vnicFcIf", "vnet", "name", "operVnetName")
	m.query("vnicFcNode", "addr")
	m.query("dcxVc", "vnic", "switchId", "transport", "operState", "linkState", "operBorderSlotId", "operBorderPortId", "operBorderAggrPortId")
	m.query("adaptorHostEthIf", "vnicDn", "operState", "linkState")
	m.query("adaptorHostFcIf", "vnicDn", "operState", "linkState")
	m.query("adaptorUnit", "model", "serial", "vendor", "operability", "thermal", "presence")
	m.query("adaptorExtEthIf", "switchId", "operState", "linkState")
	return m
}

func (m *virtual) Collect(s *module.Snapshot, e *module.Emitter) error {
	// Service profile instances, by DN and by the server they are bound to.
	instances := map[string]*ucsm.MO{}
	byServer := map[string]*ucsm.MO{}
	for _, mo := range s.Class("lsServer") {
		if mo.Get("type") != "instance" {
			continue
		}
		org, sp, ok := spOf(mo.DN)
		if !ok {
			e.Drop("unknown_dn")
			continue
		}
		instances[mo.DN] = mo
		if pn := mo.Get("pnDn"); pn != "" {
			byServer[pn] = mo
		}
		e.State(m.spAssoc, mo.Get("assocState"), org, sp)
		e.State(m.spOper, mo.Get("operState"), org, sp)
		e.State(m.spConfig, mo.Get("configState"), org, sp)
		e.Bool(m.spOK, mo.Get("operState") == "ok", org, sp)
		if t := mo.Get("operSrcTemplName"); t != "" {
			tOrg, tName, ok := spOf(t)
			if !ok {
				tOrg, tName = "unknown", t
			}
			e.Info(m.spTemplate, org, sp, tName, tOrg)
		}
	}

	// vNICs and vHBAs of service profile instances.
	for _, mo := range s.Class("vnicEther") {
		if instances[ucsm.ParentDN(mo.DN)] == nil {
			continue
		}
		d := dn.Parse(mo.DN)
		lv := []string{d.OrgPath(), d.SP, d.Vnic}
		e.Info(m.vnicInfo, append(lv, module.OrUnknown(mo.Get("addr")), module.OrUnknown(mo.Get("switchId")))...)
		e.Attr(m.vnicMTU, prometheus.GaugeValue, mo, "mtu", 0, lv...)
		if m.vlans == "off" {
			continue
		}
		vlans, down := 0, 0
		for _, vif := range s.ChildrenOfClass(mo.DN, "vnicEtherIf") {
			vlans++
			if vif.Get("operState") != "up" {
				down++
			}
			if m.vlans == "full" {
				name := vif.Get("name")
				if name == "" {
					vd := dn.Parse(vif.DN)
					name = vd.If
				}
				e.Info(m.vnicVlan, append(append([]string{}, lv...), module.OrUnknown(vif.Get("vnet")), module.OrUnknown(name), strconv.FormatBool(yes(vif.Get("defaultNet"))))...)
			}
		}
		e.Gauge(m.vnicVlans, float64(vlans), lv...)
		e.Gauge(m.vnicVlansDown, float64(down), lv...)
	}
	for _, mo := range s.Class("vnicFc") {
		spDN := ucsm.ParentDN(mo.DN)
		if instances[spDN] == nil {
			continue
		}
		d := dn.Parse(mo.DN)
		wwnn := ""
		if node := s.Get(spDN + "/fc-node"); node != nil {
			wwnn = node.Get("addr")
		}
		vsan, vsanName := "", ""
		if fcIf := s.ChildrenOfClass(mo.DN, "vnicFcIf"); len(fcIf) > 0 {
			vsan = fcIf[0].Get("vnet")
			vsanName = fcIf[0].Get("name")
			if vsanName == "" {
				vsanName = fcIf[0].Get("operVnetName")
			}
		}
		e.Info(m.vhbaInfo, d.OrgPath(), d.SP, d.Vhba, module.OrUnknown(mo.Get("addr")), module.OrUnknown(wwnn),
			module.OrUnknown(mo.Get("switchId")), module.OrUnknown(vsan), module.OrUnknown(vsanName))
	}

	// Adapter host interfaces bound to vNICs/vHBAs.
	for _, class := range []string{"adaptorHostEthIf", "adaptorHostFcIf"} {
		for _, mo := range s.Class(class) {
			lv, isFC, ok := hostIfLabels(mo)
			if !ok {
				continue
			}
			link, oper, up := m.vnicLink, m.vnicOper, m.vnicUp
			if isFC {
				link, oper, up = m.vhbaLink, m.vhbaOper, m.vhbaUp
			}
			e.State(link, mo.Get("linkState"), lv...)
			e.State(oper, mo.Get("operState"), lv...)
			e.Bool(up, mo.Get("linkState") == "up", lv...)
		}
	}

	// Virtual circuits.
	for _, mo := range s.Class("dcxVc") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || d.VC == "" || d.Mgmt {
			continue // circuits of chassis/FEX locales, monitoring sessions
		}
		sp := byServer["sys/"+server]
		name := mo.Get("vnic")
		if sp == nil || name == "" {
			// Circuits without a vNIC name are infrastructure (e.g. the
			// FCoE underlay of a vHBA); circuits of servers without a
			// service profile carry no vNIC.
			continue
		}
		org, spName, _ := spOf(sp.DN)
		fabric, ok := fabricID(mo.Get("switchId"))
		if !ok {
			fabric = d.Locale
		}
		lv := []string{server, fabric, d.VC, org, spName, name, module.OrUnknown(mo.Get("transport"))}
		e.State(m.vifOper, mo.Get("operState"), lv...)
		e.State(m.vifLink, mo.Get("linkState"), lv...)
		e.Bool(m.vifUp, mo.Get("linkState") == "up", lv...)
		slot, _ := mo.Uint("operBorderSlotId")
		port, _ := mo.Uint("operBorderPortId")
		aggr, _ := mo.Uint("operBorderAggrPortId")
		switch {
		case slot > 0 && port > 0 && aggr > 0:
			e.Info(m.vifPort, append(lv, strconv.FormatUint(slot, 10)+"/"+strconv.FormatUint(aggr, 10)+"/"+strconv.FormatUint(port, 10))...)
		case slot > 0 && port > 0:
			e.Info(m.vifPort, append(lv, strconv.FormatUint(slot, 10)+"/"+strconv.FormatUint(port, 10))...)
		case slot == 0 && port > 0:
			e.Info(m.vifPC, append(lv, strconv.FormatUint(port, 10))...)
		}
	}

	// Adapters and their uplinks.
	for _, mo := range s.Class("adaptorUnit") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || d.Adaptor == "" {
			e.Drop("unknown_dn")
			continue
		}
		if equipped(mo.Get("presence")) {
			e.Info(m.adpInfo, server, d.Adaptor, module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")), module.OrUnknown(mo.Get("vendor")))
		}
		e.State(m.adpOperability, mo.Get("operability"), server, d.Adaptor)
		e.State(m.adpThermal, mo.Get("thermal"), server, d.Adaptor)
		e.Bool(m.adpOK, mo.Get("operability") == "operable", server, d.Adaptor)
	}
	for _, mo := range s.Class("adaptorExtEthIf") {
		d := dn.Parse(mo.DN)
		server, adaptor, iface, ok := hostInterface(&d)
		if !ok || d.ExtEth == "" {
			e.Drop("unknown_dn")
			continue
		}
		fabric, ok := fabricID(mo.Get("switchId"))
		if !ok {
			fabric = "unknown"
		}
		e.Info(m.upInfo, server, adaptor, iface, fabric)
		e.State(m.upLink, mo.Get("linkState"), server, adaptor, iface)
		e.State(m.upOper, mo.Get("operState"), server, adaptor, iface)
		e.Bool(m.upUp, isUp(mo.Get("operState")), server, adaptor, iface)
	}
	return nil
}
