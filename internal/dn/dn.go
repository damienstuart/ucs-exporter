// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Package dn parses UCS Manager distinguished names into their components.
//
// A DN is a path of relative names (RNs) such as
// "sys/chassis-1/blade-3/adaptor-1/host-eth-2/vnic-stats". The meaning of an
// RN can depend on its parent ("slot-1" is an IO module under a chassis but a
// line card under a fabric interconnect), so RNs are classified using the
// kind of the preceding RN. Unrecognised RNs are recorded rather than
// rejected, and parsing never fails.
package dn

import (
	"strings"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// DN is a parsed distinguished name. String fields hold naming values, e.g.
// Chassis is "1" for "chassis-1"; empty means absent.
type DN struct {
	Raw string
	// Unknown lists unrecognised RNs other than the last one.
	Unknown []string
	// Leaf is the last RN when it is not otherwise classified, e.g.
	// "vnic-stats" or "err-stats".
	Leaf string

	// Physical hierarchy (sys/...).
	Chassis    string
	Blade      string
	RackUnit   string
	Cartridge  string
	ServerUnit string
	Fex        string
	Switch     string // fabric interconnect id: A or B
	FISlot     string // line card of a fabric interconnect
	IOM        string // IO module (slot-N) of a chassis or FEX
	PortGroup  string // host, fabric, switch-ether, switch-fc, ...
	AggrPort   string // breakout port
	Port       string
	Adaptor    string
	HostEth    string
	HostFc     string
	ExtEth     string
	ExtEthPC   string
	Board      bool
	CPU        string
	MemArray   string
	DIMM       string
	Storage    string // storage controller, e.g. "SAS-1"
	Enclosure  string
	Disk       string
	FanModule  string // "tray-id", e.g. "1-4"
	Fan        string
	PSU        string
	Locale     string // fabric-A under a server, chassis or FEX
	Path       string
	VC         string
	MgmtEntity string
	Mgmt       bool
	Firmware   string

	// Logical fabric tree (fabric/...).
	Cloud         string // lan, san, server, eth-estc, fc-estc
	CloudSide     string // A or B
	PortChannel   string
	FcoePC        string
	SubSlot       string // fabric sub-group slot-S-aggr-port-A
	SubAggr       string
	EpKind        string // phys, ep, fcoe-phys, fcoe-ep
	EpSlot        string
	EpPort        string
	ServerChassis string // fabric/server/chassis-N
	ServerSlot    string // fabric/server/chassis-N/slot-M

	// Organisations and service profiles (org-root/...).
	Orgs   []string
	SP     string
	Vnic   string
	Vhba   string
	FcNode bool
	If     string // vnicEtherIf / vnicFcIf name

	Fault string
}

type kind int

const (
	kNone kind = iota
	kSys
	kChassis
	kBlade
	kRack
	kCartridge
	kServerUnit
	kFex
	kSwitch
	kFISlot
	kIOM
	kPortGroup
	kAggrPort
	kPort
	kAdaptor
	kHostEth
	kHostFc
	kExtEth
	kBoard
	kCPU
	kMemArray
	kDIMM
	kStorage
	kEnclosure
	kDisk
	kFanModule
	kFan
	kPSU
	kLocale
	kPath
	kVC
	kMgmtEntity
	kMgmt
	kBios
	kFirmware
	kFabricRoot
	kCloud
	kCloudSide
	kPC
	kFcoePC
	kSubGroup
	kEp
	kServerChassis
	kServerSlot
	kOrg
	kSP
	kVnic
	kVhba
	kFcNode
	kIf
	kFault
	kOther
)

var portGroups = map[string]bool{
	"host": true, "fabric": true, "switch-ether": true, "switch-fc": true, "host-pc": true,
	"fabric-pc": true, "adaptor-ext": true, "adaptor-pc": true, "server-pc": true,
}

// Parse parses a DN.
func Parse(s string) DN {
	d := DN{Raw: s}
	rns := ucsm.SplitDN(s)
	prev := kNone
	for i, rn := range rns {
		k := d.classify(prev, rn)
		if k == kOther {
			if i == len(rns)-1 {
				d.Leaf = rn
			} else {
				d.Unknown = append(d.Unknown, rn)
			}
		}
		prev = k
	}
	return d
}

// digits returns the part of rn after prefix if it is a non-empty decimal
// number.
func digits(rn, prefix string) (string, bool) {
	v, ok := strings.CutPrefix(rn, prefix)
	if !ok || v == "" {
		return "", false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return "", false
		}
	}
	return v, true
}

// fabricID returns the switch id after prefix if it is a single upper-case
// letter (A, B).
func fabricID(rn, prefix string) (string, bool) {
	v, ok := strings.CutPrefix(rn, prefix)
	if !ok || len(v) != 1 || v[0] < 'A' || v[0] > 'Z' {
		return "", false
	}
	return v, true
}

// slotPort parses "<prefix>S-port-P".
func slotPort(rn, prefix string) (slot, port string, ok bool) {
	v, ok := strings.CutPrefix(rn, prefix)
	if !ok {
		return "", "", false
	}
	s, p, ok := strings.Cut(v, "-port-")
	if !ok {
		return "", "", false
	}
	if _, ok := digits("x"+s, "x"); !ok {
		return "", "", false
	}
	if _, ok := digits("x"+p, "x"); !ok {
		return "", "", false
	}
	return s, p, true
}

// subGroup parses "slot-S-aggr-port-A".
func subGroup(rn string) (slot, aggr string, ok bool) {
	v, ok := strings.CutPrefix(rn, "slot-")
	if !ok {
		return "", "", false
	}
	s, a, ok := strings.Cut(v, "-aggr-port-")
	if !ok {
		return "", "", false
	}
	if _, ok := digits("x"+s, "x"); !ok {
		return "", "", false
	}
	if _, ok := digits("x"+a, "x"); !ok {
		return "", "", false
	}
	return s, a, true
}

// fanModule parses "fan-module-T-I" into "T-I".
func fanModule(rn string) (string, bool) {
	v, ok := strings.CutPrefix(rn, "fan-module-")
	if !ok {
		return "", false
	}
	t, i, ok := strings.Cut(v, "-")
	if !ok {
		return "", false
	}
	if _, ok := digits("x"+t, "x"); !ok {
		return "", false
	}
	if _, ok := digits("x"+i, "x"); !ok {
		return "", false
	}
	return v, true
}

func (d *DN) classify(prev kind, rn string) kind {
	// RNs recognised under any parent.
	if v, ok := ucsm.RNValue(rn, "fault-"); ok {
		d.Fault = v
		return kFault
	}
	if rn == "mgmt" && prev != kNone && prev != kFabricRoot {
		d.Mgmt = true
		return kMgmt
	}
	if prev == kMgmt || prev == kBios || prev == kHostEth || prev == kHostFc || prev == kPSU ||
		prev == kStorage || prev == kDisk || prev == kSys {
		if v, ok := ucsm.RNValue(rn, "fw-"); ok {
			d.Firmware = v
			return kFirmware
		}
	}

	switch prev {
	case kNone:
		switch {
		case rn == "sys":
			return kSys
		case rn == "fabric":
			return kFabricRoot
		}
		if v, ok := ucsm.RNValue(rn, "org-"); ok {
			d.Orgs = append(d.Orgs, v)
			return kOrg
		}
	case kSys:
		if v, ok := digits(rn, "chassis-"); ok {
			d.Chassis = v
			return kChassis
		}
		if v, ok := digits(rn, "rack-unit-"); ok {
			d.RackUnit = v
			return kRack
		}
		if v, ok := digits(rn, "fex-"); ok {
			d.Fex = v
			return kFex
		}
		if v, ok := fabricID(rn, "switch-"); ok {
			d.Switch = v
			return kSwitch
		}
		if v, ok := fabricID(rn, "mgmt-entity-"); ok {
			d.MgmtEntity = v
			return kMgmtEntity
		}
	case kChassis:
		if v, ok := digits(rn, "blade-"); ok {
			d.Blade = v
			return kBlade
		}
		if v, ok := digits(rn, "cartridge-"); ok {
			d.Cartridge = v
			return kCartridge
		}
		if v, ok := digits(rn, "slot-"); ok {
			d.IOM = v
			return kIOM
		}
		if k := d.enclosureChild(rn); k != kOther {
			return k
		}
	case kFex:
		if v, ok := digits(rn, "slot-"); ok {
			d.IOM = v
			return kIOM
		}
		if v, ok := digits(rn, "fan-"); ok {
			d.Fan = v
			return kFan
		}
		if k := d.enclosureChild(rn); k != kOther {
			return k
		}
	case kSwitch:
		if v, ok := digits(rn, "slot-"); ok {
			d.FISlot = v
			return kFISlot
		}
		if v, ok := digits(rn, "fan-"); ok {
			d.Fan = v
			return kFan
		}
		if k := d.enclosureChild(rn); k != kOther {
			return k
		}
	case kCartridge:
		if v, ok := digits(rn, "server-"); ok {
			d.ServerUnit = v
			return kServerUnit
		}
	case kBlade, kRack, kServerUnit:
		if v, ok := digits(rn, "adaptor-"); ok {
			d.Adaptor = v
			return kAdaptor
		}
		switch rn {
		case "board":
			d.Board = true
			return kBoard
		case "bios":
			return kBios
		}
		if v, ok := digits(rn, "enc-"); ok {
			d.Enclosure = v
			return kEnclosure
		}
		if k := d.enclosureChild(rn); k != kOther {
			return k
		}
	case kIOM, kFISlot:
		if portGroups[rn] {
			d.PortGroup = rn
			return kPortGroup
		}
		if v, ok := fanModule(rn); ok {
			d.FanModule = v
			return kFanModule
		}
	case kPortGroup:
		if v, ok := digits(rn, "aggr-port-"); ok {
			d.AggrPort = v
			return kAggrPort
		}
		if v, ok := digits(rn, "port-"); ok {
			d.Port = v
			return kPort
		}
		if v, ok := digits(rn, "pc-"); ok {
			d.PortChannel = v
			return kPC
		}
	case kAggrPort:
		if v, ok := digits(rn, "port-"); ok {
			d.Port = v
			return kPort
		}
	case kAdaptor:
		if v, ok := digits(rn, "host-eth-"); ok {
			d.HostEth = v
			return kHostEth
		}
		if v, ok := digits(rn, "host-fc-"); ok {
			d.HostFc = v
			return kHostFc
		}
		if v, ok := digits(rn, "ext-eth-"); ok {
			d.ExtEth = v
			return kExtEth
		}
		if v, ok := digits(rn, "pc-"); ok {
			d.ExtEthPC = v
			return kPC
		}
	case kBoard:
		if v, ok := digits(rn, "cpu-"); ok {
			d.CPU = v
			return kCPU
		}
		if v, ok := digits(rn, "memarray-"); ok {
			d.MemArray = v
			return kMemArray
		}
		if v, ok := strings.CutPrefix(rn, "storage-"); ok && v != "" {
			d.Storage = v
			return kStorage
		}
	case kMemArray:
		if v, ok := digits(rn, "mem-"); ok {
			d.DIMM = v
			return kDIMM
		}
	case kStorage, kEnclosure:
		if v, ok := digits(rn, "disk-"); ok {
			d.Disk = v
			return kDisk
		}
		if v, ok := digits(rn, "enc-"); ok && prev == kStorage {
			d.Enclosure = v
			return kEnclosure
		}
	case kFanModule:
		if v, ok := digits(rn, "fan-"); ok {
			d.Fan = v
			return kFan
		}
	case kLocale:
		if v, ok := digits(rn, "path-"); ok {
			d.Path = v
			return kPath
		}
		if v, ok := digits(rn, "vc-"); ok {
			d.VC = v
			return kVC
		}
	case kPath:
		if v, ok := digits(rn, "vc-"); ok {
			d.VC = v
			return kVC
		}
	case kFabricRoot:
		switch rn {
		case "lan", "san", "server", "eth-estc", "fc-estc":
			d.Cloud = rn
			return kCloud
		}
	case kCloud:
		if d.Cloud == "server" {
			if v, ok := digits(rn, "chassis-"); ok {
				d.ServerChassis = v
				return kServerChassis
			}
			if v, ok := fabricID(rn, "sw-"); ok {
				d.CloudSide = v
				return kCloudSide
			}
		} else if len(rn) == 1 && rn[0] >= 'A' && rn[0] <= 'Z' {
			d.CloudSide = rn
			return kCloudSide
		}
	case kServerChassis:
		if v, ok := digits(rn, "slot-"); ok {
			d.ServerSlot = v
			return kServerSlot
		}
	case kCloudSide, kPC, kFcoePC, kSubGroup:
		if prev == kCloudSide {
			if v, ok := digits(rn, "pc-"); ok {
				d.PortChannel = v
				return kPC
			}
			if v, ok := digits(rn, "fcoesanpc-"); ok {
				d.FcoePC = v
				return kFcoePC
			}
		}
		if prev != kSubGroup {
			if s, a, ok := subGroup(rn); ok {
				d.SubSlot, d.SubAggr = s, a
				return kSubGroup
			}
		}
		for _, ep := range []struct{ prefix, kind string }{
			{"phys-fcoesanep-slot-", "fcoe-phys"},
			{"fcoesanpcep-slot-", "fcoe-ep"},
			{"phys-slot-", "phys"},
			{"ep-slot-", "ep"},
			{"slot-", "phys"}, // fabric/server/sw-A/slot-S-port-P
		} {
			if s, p, ok := slotPort(rn, ep.prefix); ok {
				d.EpKind, d.EpSlot, d.EpPort = ep.kind, s, p
				return kEp
			}
		}
	case kOrg:
		if v, ok := ucsm.RNValue(rn, "org-"); ok {
			d.Orgs = append(d.Orgs, v)
			return kOrg
		}
		if v, ok := ucsm.RNValue(rn, "ls-"); ok {
			d.SP = v
			return kSP
		}
	case kSP:
		if rn == "fc-node" {
			d.FcNode = true
			return kFcNode
		}
		if v, ok := ucsm.RNValue(rn, "ether-"); ok {
			d.Vnic = v
			return kVnic
		}
		if v, ok := ucsm.RNValue(rn, "fc-"); ok {
			d.Vhba = v
			return kVhba
		}
	case kVnic, kVhba:
		if v, ok := ucsm.RNValue(rn, "if-"); ok {
			d.If = v
			return kIf
		}
	}
	return kOther
}

// enclosureChild classifies RNs shared by chassis, FEX, FI and rack units.
func (d *DN) enclosureChild(rn string) kind {
	if v, ok := digits(rn, "psu-"); ok {
		d.PSU = v
		return kPSU
	}
	if v, ok := fanModule(rn); ok {
		d.FanModule = v
		return kFanModule
	}
	if v, ok := fabricID(rn, "fabric-"); ok {
		d.Locale = v
		return kLocale
	}
	return kOther
}

// Server returns the server identity: "chassis-C/blade-B", "rack-unit-R" or
// "chassis-C/cartridge-K/server-S".
func (d *DN) Server() (string, bool) {
	switch {
	case d.Chassis != "" && d.Blade != "":
		return "chassis-" + d.Chassis + "/blade-" + d.Blade, true
	case d.RackUnit != "":
		return "rack-unit-" + d.RackUnit, true
	case d.Chassis != "" && d.Cartridge != "" && d.ServerUnit != "":
		return "chassis-" + d.Chassis + "/cartridge-" + d.Cartridge + "/server-" + d.ServerUnit, true
	}
	return "", false
}

// Fabric returns the fabric side (A or B) the DN belongs to, if any.
func (d *DN) Fabric() string {
	switch {
	case d.Switch != "":
		return d.Switch
	case d.CloudSide != "":
		return d.CloudSide
	}
	return d.Locale
}

// FIPort returns a fabric interconnect port in CLI form, "slot/port" or
// "slot/aggr/port" for breakout ports, from either the physical tree
// (sys/switch-A/slot-1/switch-ether/port-17) or the logical tree
// (fabric/lan/A/phys-slot-1-port-17).
func (d *DN) FIPort() (string, bool) {
	if d.Switch != "" && d.FISlot != "" && d.Port != "" {
		if d.AggrPort != "" {
			return d.FISlot + "/" + d.AggrPort + "/" + d.Port, true
		}
		return d.FISlot + "/" + d.Port, true
	}
	if d.EpSlot != "" && d.EpPort != "" {
		if d.SubAggr != "" {
			return d.EpSlot + "/" + d.SubAggr + "/" + d.EpPort, true
		}
		return d.EpSlot + "/" + d.EpPort, true
	}
	return "", false
}

// IOPort returns an IOM or FEX port, "port" or "aggr/port".
func (d *DN) IOPort() (string, bool) {
	if d.IOM == "" || d.Port == "" {
		return "", false
	}
	if d.AggrPort != "" {
		return d.AggrPort + "/" + d.Port, true
	}
	return d.Port, true
}

// OrgPath returns the organisation path, e.g. "root/finance".
func (d *DN) OrgPath() string { return strings.Join(d.Orgs, "/") }

// Container kinds for components (PSUs, fans, sensors) that exist in several
// kinds of equipment.
const (
	ContainerNone    = ""
	ContainerServer  = "server"
	ContainerFI      = "fi"
	ContainerFex     = "fex"
	ContainerIOM     = "iom"
	ContainerChassis = "chassis"
)

// Container returns the kind of equipment a component belongs to.
func (d *DN) Container() string {
	if _, ok := d.Server(); ok {
		return ContainerServer
	}
	switch {
	case d.Switch != "":
		return ContainerFI
	case d.Fex != "":
		return ContainerFex
	case d.Chassis != "" && d.IOM != "":
		return ContainerIOM
	case d.Chassis != "":
		return ContainerChassis
	}
	return ContainerNone
}

// String returns a compact description of the recognised components, for
// tests and debugging.
func (d DN) String() string {
	var b strings.Builder
	add := func(k, v string) {
		if v == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
	}
	flag := func(k string, v bool) {
		if v {
			add(k, "true")
		}
	}
	add("chassis", d.Chassis)
	add("blade", d.Blade)
	add("rack", d.RackUnit)
	add("cartridge", d.Cartridge)
	add("server_unit", d.ServerUnit)
	add("fex", d.Fex)
	add("switch", d.Switch)
	add("fi_slot", d.FISlot)
	add("iom", d.IOM)
	add("group", d.PortGroup)
	add("aggr", d.AggrPort)
	add("port", d.Port)
	add("adaptor", d.Adaptor)
	add("host_eth", d.HostEth)
	add("host_fc", d.HostFc)
	add("ext_eth", d.ExtEth)
	add("ext_eth_pc", d.ExtEthPC)
	flag("board", d.Board)
	add("cpu", d.CPU)
	add("memarray", d.MemArray)
	add("dimm", d.DIMM)
	add("storage", d.Storage)
	add("enc", d.Enclosure)
	add("disk", d.Disk)
	add("fan_module", d.FanModule)
	add("fan", d.Fan)
	add("psu", d.PSU)
	add("locale", d.Locale)
	add("path", d.Path)
	add("vc", d.VC)
	add("mgmt_entity", d.MgmtEntity)
	flag("mgmt", d.Mgmt)
	add("fw", d.Firmware)
	add("cloud", d.Cloud)
	add("side", d.CloudSide)
	add("pc", d.PortChannel)
	add("fcoe_pc", d.FcoePC)
	add("sub_slot", d.SubSlot)
	add("sub_aggr", d.SubAggr)
	add("ep_kind", d.EpKind)
	add("ep_slot", d.EpSlot)
	add("ep_port", d.EpPort)
	add("srv_chassis", d.ServerChassis)
	add("srv_slot", d.ServerSlot)
	add("org", d.OrgPath())
	add("sp", d.SP)
	add("vnic", d.Vnic)
	add("vhba", d.Vhba)
	flag("fc_node", d.FcNode)
	add("if", d.If)
	add("fault", d.Fault)
	add("leaf", d.Leaf)
	if len(d.Unknown) > 0 {
		add("unknown", strings.Join(d.Unknown, ","))
	}
	return b.String()
}
