// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package modules

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// Port entity kinds. Each has its own metric prefix and label set.
const (
	kindFIPort            = "fi_port"
	kindFIPortChannel     = "fi_port_channel"
	kindFIFCPort          = "fi_fc_port"
	kindFIFCPortChannel   = "fi_fc_port_channel"
	kindFCoEUplink        = "fi_fcoe_uplink"
	kindFCoEPortChannel   = "fi_fcoe_port_channel"
	kindIOMHostPort       = "iom_host_port"
	kindIOMFabricPort     = "iom_fabric_port"
	kindFexHostPort       = "fex_host_port"
	kindFexFabricPort     = "fex_fabric_port"
	labelFabric           = "fabric"
	labelPort             = "port"
	labelPortChannel      = "port_channel"
	labelState            = "state"
	labelServer           = "server"
	labelChassis          = "chassis"
	labelIOM              = "iom"
	labelFex              = "fex"
	labelAdaptor          = "adaptor"
	labelInterface        = "interface"
	labelOrg              = "org"
	labelServiceProfile   = "service_profile"
	labelVnic             = "vnic"
	labelVhba             = "vhba"
	labelSensor           = "sensor"
	containerPrefixServer = "ucs_server"
)

type family struct {
	prefix string
	labels []string
}

var families = map[string]family{
	kindFIPort:          {"ucs_fi_port", []string{labelFabric, labelPort, "role"}},
	kindFIPortChannel:   {"ucs_fi_port_channel", []string{labelFabric, labelPortChannel}},
	kindFIFCPort:        {"ucs_fi_fc_port", []string{labelFabric, labelPort}},
	kindFIFCPortChannel: {"ucs_fi_fc_port_channel", []string{labelFabric, labelPortChannel}},
	kindFCoEUplink:      {"ucs_fi_fcoe_uplink", []string{labelFabric, labelPort}},
	kindFCoEPortChannel: {"ucs_fi_fcoe_port_channel", []string{labelFabric, labelPortChannel}},
	kindIOMHostPort:     {"ucs_iom_host_port", []string{labelChassis, labelIOM, labelPort}},
	kindIOMFabricPort:   {"ucs_iom_fabric_port", []string{labelChassis, labelIOM, labelPort}},
	kindFexHostPort:     {"ucs_fex_host_port", []string{labelFex, labelPort}},
	kindFexFabricPort:   {"ucs_fex_fabric_port", []string{labelFex, labelPort}},
}

// portEntity identifies the port or port channel with DN portDN (for
// statistics, the parent of the statistics object). role is looked up from
// the port object for FI Ethernet ports.
func portEntity(s *module.Snapshot, portDN string) (kind string, lv []string, ok bool) {
	d := dn.Parse(portDN)
	switch {
	case d.Switch != "" && d.FISlot != "" && d.Port != "":
		port, _ := d.FIPort()
		switch d.PortGroup {
		case "switch-ether":
			role := ""
			if mo := s.Get(portDN); mo != nil {
				role = mo.Get("ifRole")
			}
			return kindFIPort, []string{d.Switch, port, module.OrUnknown(role)}, true
		case "switch-fc":
			return kindFIFCPort, []string{d.Switch, port}, true
		}
	case d.CloudSide != "" && d.PortChannel != "" && d.EpKind == "":
		switch d.Cloud {
		case "lan":
			return kindFIPortChannel, []string{d.CloudSide, d.PortChannel}, true
		case "san":
			return kindFIFCPortChannel, []string{d.CloudSide, d.PortChannel}, true
		}
	case d.Cloud == "san" && d.CloudSide != "" && d.EpKind == "fcoe-phys":
		port, _ := d.FIPort()
		return kindFCoEUplink, []string{d.CloudSide, port}, true
	case d.Cloud == "san" && d.CloudSide != "" && d.FcoePC != "" && d.EpKind == "":
		return kindFCoEPortChannel, []string{d.CloudSide, d.FcoePC}, true
	case d.IOM != "" && d.Port != "":
		port, _ := d.IOPort()
		switch {
		case d.Chassis != "" && d.PortGroup == "host":
			return kindIOMHostPort, []string{d.Chassis, d.IOM, port}, true
		case d.Chassis != "" && d.PortGroup == "fabric":
			return kindIOMFabricPort, []string{d.Chassis, d.IOM, port}, true
		case d.Fex != "" && d.PortGroup == "host":
			return kindFexHostPort, []string{d.Fex, port}, true
		case d.Fex != "" && d.PortGroup == "fabric":
			return kindFexFabricPort, []string{d.Fex, port}, true
		}
	}
	return "", nil, false
}

// Container kinds for components found in several kinds of equipment.
type container struct {
	prefix string
	labels []string
}

var containers = map[string]container{
	dn.ContainerServer:  {containerPrefixServer, []string{labelServer}},
	dn.ContainerChassis: {"ucs_chassis", []string{labelChassis}},
	dn.ContainerIOM:     {"ucs_iom", []string{labelChassis, labelIOM}},
	dn.ContainerFex:     {"ucs_fex", []string{labelFex}},
	dn.ContainerFI:      {"ucs_fi", []string{labelFabric}},
}

var allContainers = []string{dn.ContainerServer, dn.ContainerChassis, dn.ContainerIOM, dn.ContainerFex, dn.ContainerFI}

// containerOf returns the container kind and identity label values of a
// component DN.
func containerOf(d *dn.DN) (kind string, lv []string, ok bool) {
	switch k := d.Container(); k {
	case dn.ContainerServer:
		s, _ := d.Server()
		return k, []string{s}, true
	case dn.ContainerChassis:
		return k, []string{d.Chassis}, true
	case dn.ContainerIOM:
		return k, []string{d.Chassis, d.IOM}, true
	case dn.ContainerFex:
		return k, []string{d.Fex}, true
	case dn.ContainerFI:
		return k, []string{d.Switch}, true
	}
	return "", nil, false
}

// fanID returns "tray-id/fan" for a fan in a fan module, or the fan number.
func fanID(d *dn.DN) string {
	if d.FanModule != "" {
		return d.FanModule + "/" + d.Fan
	}
	return d.Fan
}

// hostInterface identifies an adaptor host interface: server, adaptor and
// interface RN.
func hostInterface(d *dn.DN) (server, adaptor, iface string, ok bool) {
	server, ok = d.Server()
	if !ok || d.Adaptor == "" {
		return "", "", "", false
	}
	switch {
	case d.HostEth != "":
		return server, d.Adaptor, "host-eth-" + d.HostEth, true
	case d.HostFc != "":
		return server, d.Adaptor, "host-fc-" + d.HostFc, true
	case d.ExtEth != "":
		return server, d.Adaptor, "ext-eth-" + d.ExtEth, true
	}
	return "", "", "", false
}

// vnicOwner resolves the service profile owning an adaptor host interface
// from its vnicDn ("org-root/org-X/ls-NAME/ether-eth0"): org path, service
// profile and vNIC or vHBA name.
func vnicOwner(vnicDn string) (org, sp, name string, ok bool) {
	if vnicDn == "" {
		return "", "", "", false
	}
	d := dn.Parse(vnicDn)
	name = d.Vnic
	if name == "" {
		name = d.Vhba
	}
	if d.SP == "" || name == "" || len(d.Orgs) == 0 {
		return "", "", "", false
	}
	return d.OrgPath(), d.SP, name, true
}

// isUp reports whether a port operState means the link is up.
func isUp(state string) bool { return state == "up" || state == "link-up" }

// yes reports whether a UCSM boolean attribute is true.
func yes(v string) bool {
	switch strings.ToLower(v) {
	case "yes", "true", "enabled":
		return true
	}
	return false
}

// equipped reports whether a presence value means the component is present.
func equipped(presence string) bool { return strings.HasPrefix(presence, "equipped") }

// parseSpeed converts "10gbps"-style speeds to bytes per second.
func parseSpeed(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, u := range []struct {
		suffix string
		mult   float64
	}{{"gbps", 1e9}, {"mbps", 1e6}, {"kbps", 1e3}} {
		if v, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || n <= 0 {
				return 0, false
			}
			return n * u.mult / 8, true
		}
	}
	return 0, false
}

// parseGbps converts a plain number of gigabits per second to bytes per
// second.
func parseGbps(s string) (float64, bool) {
	n, ok := ucsm.ParseNumber(s)
	if !ok || n <= 0 {
		return 0, false
	}
	return n * 1e9 / 8, true
}

// parseUptime converts UCSM uptimes ("DD:HH:MM:SS", "HH:MM:SS", "MM:SS" or
// "SS") to seconds.
func parseUptime(s string) (float64, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) == 0 || len(parts) > 4 {
		return 0, false
	}
	mult := []float64{1, 60, 3600, 86400}
	total := 0.0
	for i := range parts {
		n, err := strconv.ParseUint(parts[len(parts)-1-i], 10, 64)
		if err != nil {
			return 0, false
		}
		total += float64(n) * mult[i]
	}
	return total, true
}

// positive parses a number and rejects zero and negative values, used for
// sensors that report 0 when absent.
func positive(s string) (float64, bool) {
	v, ok := ucsm.ParseNumber(s)
	return v, ok && v > 0
}

// snake converts a camelCase attribute name to snake_case:
// "mainBoardOutlet1" -> "main_board_outlet1", "SlotOutlet1" -> "slot_outlet1".
func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fabricID returns a UCSM switchId if it is a fabric side (A or B).
func fabricID(v string) (string, bool) {
	if v == "A" || v == "B" {
		return v, true
	}
	return "", false
}
