// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package modules

import (
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// sensor maps a temperature attribute to a sensor label value.
type sensor struct{ attr, name string }

var (
	bladeBoardSensors = []sensor{
		{"fmTempSenIo", "io"}, {"fmTempSenRear", "rear"}, {"fmTempSenRearL", "rear_left"},
		{"fmTempSenRearR", "rear_right"}, {"fmTempSenFrontR", "front_right"},
	}
	rackBoardSensors = []sensor{
		{"ambientTemp", "ambient"}, {"frontTemp", "front"}, {"rearTemp", "rear"}, {"ioh1Temp", "ioh1"}, {"ioh2Temp", "ioh2"},
	}
	iomSensors = []sensor{{"ambientTemp", "ambient"}, {"dimmTemp", "dimm"}, {"procTemp", "processor"}, {"temp", "temp"}}
	fiSensors  = sensorsFor("donner", "fanCtrlrInlet1", "fanCtrlrInlet2", "fanCtrlrInlet3", "fanCtrlrInlet4",
		"mainBoardOutlet1", "mainBoardOutlet2", "psuCtrlrInlet1", "psuCtrlrInlet2", "td2", "tiburon")
	fiCardSensors = sensorsFor("SlotOutlet1", "SlotOutlet2", "SlotOutlet3")
	psuSensors    = []sensor{{"ambientTemp", "ambient"}, {"psuTemp1", "psu1"}, {"psuTemp2", "psu2"}}

	psuContainers       = []string{dn.ContainerServer, dn.ContainerChassis, dn.ContainerFex, dn.ContainerFI}
	fanModuleContainers = []string{dn.ContainerServer, dn.ContainerChassis, dn.ContainerIOM, dn.ContainerFI}
)

func sensorsFor(attrs ...string) []sensor {
	out := make([]sensor, len(attrs))
	for i, a := range attrs {
		out[i] = sensor{a, snake(a)}
	}
	return out
}

func sensorAttrs(ss []sensor) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.attr
	}
	return out
}

// emitSensors emits one gauge per sensor with a positive reading (sensors
// that are absent read 0).
func emitSensors(e *module.Emitter, d *prometheus.Desc, mo *ucsm.MO, ss []sensor, lv ...string) {
	for _, s := range ss {
		if v, ok := positive(mo.Get(s.attr)); ok {
			e.Gauge(d, v, slices.Concat(lv, []string{s.name})...)
		}
	}
}

type hardware struct {
	base
	firmwareTypes map[string]bool

	srvOper, srvOperability, srvPower, srvPresence, srvAssoc, srvThermal, srvOK, srvPoweredOn *prometheus.Desc
	boardTemp                                                                                 *prometheus.Desc
	cpuInfo, cpuOperability, cpuOK                                                            *prometheus.Desc
	dimmInfo, dimmOperability, dimmOK, dimmTemp, dimmSize                                     *prometheus.Desc
	diskInfo, diskOperability, diskDrive, diskOK, diskSize                                    *prometheus.Desc

	chInfo, chOperability, chThermal, chPower, chOK *prometheus.Desc
	chStats                                         *module.Table
	iomInfo, iomOperability, iomThermal, iomOK      *prometheus.Desc
	iomActive, iomTemp                              *prometheus.Desc
	iomStats                                        *module.Table
	fexInfo, fexOperability, fexOK                  *prometheus.Desc
	fiInfo, fiOperability, fiThermal, fiOK, fiEvac  *prometheus.Desc
	fiMemory, fiTemp                                *prometheus.Desc

	mgmtLeader, mgmtState, mgmtHAReadiness, mgmtServices, mgmtUmbilical *prometheus.Desc
	mgmtHAReady, mgmtVersionMismatch, mgmtUp                            *prometheus.Desc

	psuInfo, psuOperability, psuPower, psuThermal, psuVoltage, psuPresence, psuOK map[string]*prometheus.Desc
	psuCapacity, psuInPower, psuInVoltage, psuOutPower, psuOutCurrent, psuI2C     map[string]*prometheus.Desc
	psuOutVoltage, psuTemp                                                        map[string]*prometheus.Desc

	fanOperability, fanOK       map[string]*prometheus.Desc
	fmOperability, fmOK, fmTemp map[string]*prometheus.Desc
	firmware                    *prometheus.Desc
}

func newHardware(opts config.Options) *hardware {
	m := &hardware{
		base:          base{name: "hardware", description: "Health and inventory of servers, CPUs, DIMMs, disks, chassis, IOMs, FEXes, fabric interconnects (including HA state), PSUs, fans, temperatures and firmware versions."},
		firmwareTypes: map[string]bool{},
	}
	for _, t := range opts.FirmwareTypes {
		m.firmwareTypes[t] = true
	}
	st := func(ls ...string) []string { return append(ls, labelState) }

	// Servers.
	srv := []string{labelServer}
	m.srvOper = m.desc("ucs_server_oper_state", "Server operational state (UCSM computeBlade/computeRackUnit.operState).", st(srv...)...)
	m.srvOperability = m.desc("ucs_server_operability_state", "Server operability (UCSM computeBlade/computeRackUnit.operability).", st(srv...)...)
	m.srvPower = m.desc("ucs_server_power_state", "Server power state (UCSM computeBlade/computeRackUnit.operPower).", st(srv...)...)
	m.srvPresence = m.desc("ucs_server_presence_state", "Server presence (UCSM computeBlade/computeRackUnit.presence).", st(srv...)...)
	m.srvAssoc = m.desc("ucs_server_association_state", "Server association with a service profile (UCSM computeBlade/computeRackUnit.association).", st(srv...)...)
	m.srvThermal = m.desc("ucs_server_thermal_state", "Server thermal state (UCSM computeBlade/computeRackUnit.thermal).", st(srv...)...)
	m.srvOK = m.desc("ucs_server_ok", "Whether the server is operable (UCSM computeBlade/computeRackUnit.operability).", srv...)
	m.srvPoweredOn = m.desc("ucs_server_powered_on", "Whether the server is powered on (UCSM computeBlade/computeRackUnit.operPower).", srv...)
	for _, c := range []string{"computeBlade", "computeRackUnit"} {
		m.query(c, "operState", "operability", "operPower", "presence", "association", "thermal")
	}
	m.boardTemp = m.desc("ucs_server_board_temperature_celsius", "Server motherboard temperature sensors in degrees Celsius (UCSM computeMbTempStats, computeRackUnitMbTempStats).", labelServer, labelSensor)
	m.query("computeMbTempStats", sensorAttrs(bladeBoardSensors)...)
	m.query("computeRackUnitMbTempStats", sensorAttrs(rackBoardSensors)...)

	cpu := []string{labelServer, "cpu"}
	m.cpuInfo = m.desc("ucs_server_cpu_info", "CPU information (UCSM processorUnit).", append(slices.Clone(cpu), "model")...)
	m.cpuOperability = m.desc("ucs_server_cpu_operability_state", "CPU operability (UCSM processorUnit.operability).", st(slices.Clone(cpu)...)...)
	m.cpuOK = m.desc("ucs_server_cpu_ok", "Whether the CPU is operable (UCSM processorUnit.operability).", cpu...)
	m.query("processorUnit", "model", "operability", "presence")

	dimm := []string{labelServer, "memory_array", "dimm"}
	m.dimmInfo = m.desc("ucs_server_dimm_info", "DIMM information (UCSM memoryUnit).", append(slices.Clone(dimm), "location", "type", "model", "serial")...)
	m.dimmOperability = m.desc("ucs_server_dimm_operability_state", "DIMM operability (UCSM memoryUnit.operability).", st(slices.Clone(dimm)...)...)
	m.dimmOK = m.desc("ucs_server_dimm_ok", "Whether the DIMM is operable (UCSM memoryUnit.operability).", dimm...)
	m.dimmSize = m.desc("ucs_server_dimm_size_bytes", "DIMM capacity in bytes (UCSM memoryUnit.capacity).", dimm...)
	m.dimmTemp = m.desc("ucs_server_dimm_temperature_celsius", "DIMM temperature in degrees Celsius (UCSM memoryUnitEnvStats.temperature).", dimm...)
	m.query("memoryUnit", "location", "type", "model", "serial", "operability", "presence", "capacity")
	m.query("memoryUnitEnvStats", "temperature")

	disk := []string{labelServer, "disk"}
	m.diskInfo = m.desc("ucs_server_disk_info", "Local disk information (UCSM storageLocalDisk).", append(slices.Clone(disk), "model", "serial", "device_type", "protocol")...)
	m.diskOperability = m.desc("ucs_server_disk_operability_state", "Local disk operability (UCSM storageLocalDisk.operability).", st(slices.Clone(disk)...)...)
	m.diskDrive = m.desc("ucs_server_disk_drive_state", "Local disk drive state (UCSM storageLocalDisk.diskState).", st(slices.Clone(disk)...)...)
	m.diskOK = m.desc("ucs_server_disk_ok", "Whether the local disk is operable (UCSM storageLocalDisk.operability).", disk...)
	m.diskSize = m.desc("ucs_server_disk_size_bytes", "Local disk size in bytes, from megabytes (UCSM storageLocalDisk.size).", disk...)
	m.query("storageLocalDisk", "model", "serial", "deviceType", "connectionProtocol", "operability", "diskState", "size", "presence")

	// Chassis.
	ch := []string{labelChassis}
	m.chInfo = m.desc("ucs_chassis_info", "Chassis information (UCSM equipmentChassis).", labelChassis, "model", "serial")
	m.chOperability = m.desc("ucs_chassis_operability_state", "Chassis operability (UCSM equipmentChassis.operability).", st(labelChassis)...)
	m.chThermal = m.desc("ucs_chassis_thermal_state", "Chassis thermal state (UCSM equipmentChassis.thermal).", st(labelChassis)...)
	m.chPower = m.desc("ucs_chassis_power_state", "Chassis power (redundancy) state (UCSM equipmentChassis.power).", st(labelChassis)...)
	m.chOK = m.desc("ucs_chassis_ok", "Whether the chassis is operable (UCSM equipmentChassis.operability).", ch...)
	m.query("equipmentChassis", "model", "serial", "operability", "thermal", "power")
	m.chStats = m.table("equipmentChassisStats", "ucs_chassis", ch,
		module.G("inputPower", "input_power_watts", "Chassis input power in watts"),
		module.G("outputPower", "output_power_watts", "Chassis output power in watts"),
		module.C("ChassisI2CErrors", "i2c_errors_total", "Chassis I2C errors"),
	)

	// IO modules.
	iom := []string{labelChassis, labelIOM}
	m.iomInfo = m.desc("ucs_iom_info", "IO module information (UCSM equipmentIOCard).", append(slices.Clone(iom), "model", "serial", labelFabric)...)
	m.iomOperability = m.desc("ucs_iom_operability_state", "IO module operability (UCSM equipmentIOCard.operability).", st(slices.Clone(iom)...)...)
	m.iomThermal = m.desc("ucs_iom_thermal_state", "IO module thermal state (UCSM equipmentIOCard.thermal).", st(slices.Clone(iom)...)...)
	m.iomOK = m.desc("ucs_iom_ok", "Whether the IO module is operable (UCSM equipmentIOCard.operability).", iom...)
	m.iomActive = m.desc("ucs_iom_active_fabric_ports", "Active fabric (IOM to FI) ports of the IO module (UCSM equipmentIOCard.numOfActiveFabricPorts).", iom...)
	m.iomTemp = m.desc("ucs_iom_temperature_celsius", "IO module temperatures in degrees Celsius (UCSM equipmentIOCardStats).", append(slices.Clone(iom), labelSensor)...)
	m.query("equipmentIOCard", "model", "serial", "switchId", "operability", "thermal", "numOfActiveFabricPorts")
	m.query("equipmentIOCardStats", sensorAttrs(iomSensors)...)
	m.iomStats = m.table("equipmentIOCardStats", "ucs_iom", iom, module.C("IomI2CErrors", "i2c_errors_total", "IO module I2C errors"))

	// FEX.
	m.fexInfo = m.desc("ucs_fex_info", "FEX information (UCSM equipmentFex).", labelFex, "model", "serial", labelFabric)
	m.fexOperability = m.desc("ucs_fex_operability_state", "FEX operability (UCSM equipmentFex.operability).", st(labelFex)...)
	m.fexOK = m.desc("ucs_fex_ok", "Whether the FEX is operable (UCSM equipmentFex.operability).", labelFex)
	m.query("equipmentFex", "model", "serial", "switchId", "operability")

	// Fabric interconnects.
	m.fiInfo = m.desc("ucs_fi_info", "Fabric interconnect information (UCSM networkElement).", labelFabric, "model", "serial")
	m.fiOperability = m.desc("ucs_fi_operability_state", "Fabric interconnect operability (UCSM networkElement.operability).", st(labelFabric)...)
	m.fiThermal = m.desc("ucs_fi_thermal_state", "Fabric interconnect thermal state (UCSM networkElement.thermal).", st(labelFabric)...)
	m.fiOK = m.desc("ucs_fi_ok", "Whether the fabric interconnect is operable (UCSM networkElement.operability).", labelFabric)
	m.fiEvac = m.desc("ucs_fi_evacuation_state", "Fabric interconnect traffic evacuation state (UCSM networkElement.operEvacState).", st(labelFabric)...)
	m.fiMemory = m.desc("ucs_fi_memory_bytes", "Fabric interconnect installed memory in bytes (UCSM networkElement.totalMemory).", labelFabric)
	m.fiTemp = m.desc("ucs_fi_temperature_celsius", "Fabric interconnect temperature sensors in degrees Celsius (UCSM swEnvStats, swCardEnvStats).", labelFabric, labelSensor)
	m.query("networkElement", "model", "serial", "operability", "thermal", "operEvacState", "totalMemory")
	m.query("swEnvStats", sensorAttrs(fiSensors)...)
	m.query("swCardEnvStats", sensorAttrs(fiCardSensors)...)

	// Cluster (HA) state.
	m.mgmtLeader = m.desc("ucs_fi_mgmt_leadership_state", "UCS Manager cluster role of the fabric interconnect (UCSM mgmtEntity.leadership).", st(labelFabric)...)
	m.mgmtState = m.desc("ucs_fi_mgmt_state", "UCS Manager instance state on the fabric interconnect (UCSM mgmtEntity.state).", st(labelFabric)...)
	m.mgmtHAReadiness = m.desc("ucs_fi_mgmt_ha_readiness_state", "UCS Manager HA readiness (UCSM mgmtEntity.haReadiness).", st(labelFabric)...)
	m.mgmtServices = m.desc("ucs_fi_mgmt_services_state", "UCS Manager services state (UCSM mgmtEntity.mgmtServicesState).", st(labelFabric)...)
	m.mgmtUmbilical = m.desc("ucs_fi_mgmt_umbilical_state", "State of the link between the fabric interconnects (UCSM mgmtEntity.umbilicalState).", st(labelFabric)...)
	m.mgmtHAReady = m.desc("ucs_fi_mgmt_ha_ready", "Whether the cluster is ready for failover (UCSM mgmtEntity.haReady).", labelFabric)
	m.mgmtVersionMismatch = m.desc("ucs_fi_mgmt_version_mismatch", "Whether the fabric interconnects run different UCS Manager versions (UCSM mgmtEntity.versionMismatch).", labelFabric)
	m.mgmtUp = m.desc("ucs_fi_mgmt_up", "Whether the UCS Manager instance on the fabric interconnect is up (UCSM mgmtEntity.state).", labelFabric)
	m.query("mgmtEntity", "leadership", "state", "haReadiness", "mgmtServicesState", "umbilicalState", "haReady", "versionMismatch")

	// PSUs.
	m.psuInfo = m.containerDescs(psuContainers, "psu_info", "Power supply information (UCSM equipmentPsu).", "psu", "model", "serial")
	m.psuOperability = m.containerDescs(psuContainers, "psu_operability_state", "Power supply operability (UCSM equipmentPsu.operability).", "psu", labelState)
	m.psuPower = m.containerDescs(psuContainers, "psu_power_state", "Power supply power state (UCSM equipmentPsu.power).", "psu", labelState)
	m.psuThermal = m.containerDescs(psuContainers, "psu_thermal_state", "Power supply thermal state (UCSM equipmentPsu.thermal).", "psu", labelState)
	m.psuVoltage = m.containerDescs(psuContainers, "psu_voltage_state", "Power supply voltage state (UCSM equipmentPsu.voltage).", "psu", labelState)
	m.psuPresence = m.containerDescs(psuContainers, "psu_presence_state", "Power supply presence (UCSM equipmentPsu.presence).", "psu", labelState)
	m.psuOK = m.containerDescs(psuContainers, "psu_ok", "Whether the power supply is operable (UCSM equipmentPsu.operability).", "psu")
	m.psuCapacity = m.containerDescs(psuContainers, "psu_capacity_watts", "Power supply rated capacity in watts (UCSM equipmentPsu.psuWattage).", "psu")
	m.psuInPower = m.containerDescs(psuContainers, "psu_input_power_watts", "Power supply input power in watts (UCSM equipmentPsuStats.inputPower).", "psu")
	m.psuInVoltage = m.containerDescs(psuContainers, "psu_input_voltage_volts", "Power supply input voltage in volts (UCSM equipmentPsuStats.input210v).", "psu")
	m.psuOutPower = m.containerDescs(psuContainers, "psu_output_power_watts", "Power supply output power in watts (UCSM equipmentPsuStats.outputPower).", "psu")
	m.psuOutCurrent = m.containerDescs(psuContainers, "psu_output_current_amperes", "Power supply output current in amperes (UCSM equipmentPsuStats.outputCurrent).", "psu")
	m.psuOutVoltage = m.containerDescs(psuContainers, "psu_output_voltage_volts", "Power supply output voltage in volts by rail (UCSM equipmentPsuStats.output12v, output3v3).", "psu", "rail")
	m.psuTemp = m.containerDescs(psuContainers, "psu_temperature_celsius", "Power supply temperatures in degrees Celsius (UCSM equipmentPsuStats).", "psu", labelSensor)
	m.psuI2C = m.containerDescs(psuContainers, "psu_i2c_errors_total", "Power supply I2C errors (UCSM equipmentPsuStats.PsuI2CErrors).", "psu")
	m.query("equipmentPsu", "model", "serial", "operability", "power", "thermal", "voltage", "presence", "psuWattage")
	m.query("equipmentPsuStats", "inputPower", "input210v", "outputPower", "outputCurrent", "output12v", "output3v3", "PsuI2CErrors")
	m.query("equipmentPsuStats", sensorAttrs(psuSensors)...)

	// Fans and fan modules.
	m.fanOperability = m.containerDescs(allContainers, "fan_operability_state", "Fan operability (UCSM equipmentFan.operability).", "fan", labelState)
	m.fanOK = m.containerDescs(allContainers, "fan_ok", "Whether the fan is operable (UCSM equipmentFan.operability).", "fan")
	m.fmOperability = m.containerDescs(fanModuleContainers, "fan_module_operability_state", "Fan module operability (UCSM equipmentFanModule.operability).", "fan_module", labelState)
	m.fmOK = m.containerDescs(fanModuleContainers, "fan_module_ok", "Whether the fan module is operable (UCSM equipmentFanModule.operability).", "fan_module")
	m.fmTemp = m.containerDescs(fanModuleContainers, "fan_module_temperature_celsius", "Fan module ambient temperature in degrees Celsius (UCSM equipmentFanModuleStats.ambientTemp).", "fan_module")
	m.query("equipmentFan", "operability")
	m.query("equipmentFanModule", "operability")
	m.query("equipmentFanModuleStats", "ambientTemp")

	m.firmware = m.desc("ucs_firmware_running_info", "Running firmware version of a component, e.g. component=\"chassis-1/blade-3\" type=\"blade-controller\" (UCSM firmwareRunning).", "component", "type", "deployment", "version")
	m.query("firmwareRunning", "type", "deployment", "version")
	return m
}

func (m *hardware) Collect(s *module.Snapshot, e *module.Emitter) error {
	m.collectServers(s, e)
	m.collectChassis(s, e)
	m.collectFI(s, e)
	m.collectPSUs(s, e)
	m.collectFans(s, e)
	for _, mo := range s.Class("firmwareRunning") {
		typ := mo.Get("type")
		if !m.firmwareTypes[typ] {
			continue
		}
		e.Info(m.firmware, firmwareComponent(mo.DN), typ, module.OrUnknown(mo.Get("deployment")), module.OrUnknown(mo.Get("version")))
	}
	return nil
}

func (m *hardware) collectServers(s *module.Snapshot, e *module.Emitter) {
	for _, class := range []string{"computeBlade", "computeRackUnit"} {
		for _, mo := range s.Class(class) {
			d := dn.Parse(mo.DN)
			server, ok := d.Server()
			if !ok {
				e.Drop("unknown_dn")
				continue
			}
			e.State(m.srvOper, mo.Get("operState"), server)
			e.State(m.srvOperability, mo.Get("operability"), server)
			e.State(m.srvPower, mo.Get("operPower"), server)
			e.State(m.srvPresence, mo.Get("presence"), server)
			e.State(m.srvAssoc, mo.Get("association"), server)
			e.State(m.srvThermal, mo.Get("thermal"), server)
			e.Bool(m.srvOK, mo.Get("operability") == "operable", server)
			e.Bool(m.srvPoweredOn, mo.Get("operPower") == "on", server)
		}
	}
	for class, sensors := range map[string][]sensor{"computeMbTempStats": bladeBoardSensors, "computeRackUnitMbTempStats": rackBoardSensors} {
		for _, mo := range s.Class(class) {
			d := dn.Parse(mo.DN)
			server, ok := d.Server()
			if !ok || !d.Board {
				e.Drop("unknown_dn")
				continue
			}
			emitSensors(e, m.boardTemp, mo, sensors, server)
		}
	}
	for _, mo := range s.Class("processorUnit") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || d.CPU == "" {
			e.Drop("unknown_dn")
			continue
		}
		if !equipped(mo.Get("presence")) {
			continue
		}
		e.Info(m.cpuInfo, server, d.CPU, module.OrUnknown(mo.Get("model")))
		e.State(m.cpuOperability, mo.Get("operability"), server, d.CPU)
		e.Bool(m.cpuOK, mo.Get("operability") == "operable", server, d.CPU)
	}
	for _, mo := range s.Class("memoryUnit") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || d.MemArray == "" || d.DIMM == "" {
			e.Drop("unknown_dn")
			continue
		}
		if !equipped(mo.Get("presence")) {
			continue
		}
		lv := []string{server, d.MemArray, d.DIMM}
		e.Info(m.dimmInfo, append(slices.Clone(lv), module.OrUnknown(mo.Get("location")), module.OrUnknown(mo.Get("type")),
			module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")))...)
		e.State(m.dimmOperability, mo.Get("operability"), lv...)
		e.Bool(m.dimmOK, mo.Get("operability") == "operable", lv...)
		if v, ok := positive(mo.Get("capacity")); ok {
			e.Gauge(m.dimmSize, v*module.MiB, lv...)
		}
	}
	for _, mo := range s.Class("memoryUnitEnvStats") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || d.MemArray == "" || d.DIMM == "" {
			e.Drop("unknown_dn")
			continue
		}
		if unit := s.Parent(mo); unit != nil && !equipped(unit.Get("presence")) {
			continue
		}
		if v, ok := positive(mo.Get("temperature")); ok {
			e.Gauge(m.dimmTemp, v, server, d.MemArray, d.DIMM)
		}
	}
	for _, mo := range s.Class("storageLocalDisk") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || d.Disk == "" {
			e.Drop("unknown_dn")
			continue
		}
		if p := mo.Get("presence"); p != "" && !equipped(p) {
			continue
		}
		var disk string
		switch {
		case d.Storage != "":
			disk = d.Storage + "/" + d.Disk
		case d.Enclosure != "":
			disk = "enc-" + d.Enclosure + "/" + d.Disk
		default:
			disk = d.Disk
		}
		e.Info(m.diskInfo, server, disk, module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")),
			module.OrUnknown(mo.Get("deviceType")), module.OrUnknown(mo.Get("connectionProtocol")))
		e.State(m.diskOperability, mo.Get("operability"), server, disk)
		e.State(m.diskDrive, mo.Get("diskState"), server, disk)
		e.Bool(m.diskOK, mo.Get("operability") == "operable", server, disk)
		if v, ok := positive(mo.Get("size")); ok {
			e.Gauge(m.diskSize, v*module.MiB, server, disk)
		}
	}
}

func (m *hardware) collectChassis(s *module.Snapshot, e *module.Emitter) {
	for _, mo := range s.Class("equipmentChassis") {
		d := dn.Parse(mo.DN)
		if d.Chassis == "" {
			e.Drop("unknown_dn")
			continue
		}
		e.Info(m.chInfo, d.Chassis, module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")))
		e.State(m.chOperability, mo.Get("operability"), d.Chassis)
		e.State(m.chThermal, mo.Get("thermal"), d.Chassis)
		e.State(m.chPower, mo.Get("power"), d.Chassis)
		e.Bool(m.chOK, mo.Get("operability") == "operable", d.Chassis)
	}
	for _, mo := range s.Class("equipmentChassisStats") {
		d := dn.Parse(mo.DN)
		if d.Chassis == "" || d.IOM != "" {
			e.Drop("unknown_dn")
			continue
		}
		m.chStats.Emit(e, mo, d.Chassis)
	}
	for _, mo := range s.Class("equipmentIOCard") {
		d := dn.Parse(mo.DN)
		if d.Chassis == "" || d.IOM == "" {
			continue // FEX IO modules are reported as the FEX
		}
		fabric, ok := fabricID(mo.Get("switchId"))
		if !ok {
			fabric = "unknown"
		}
		e.Info(m.iomInfo, d.Chassis, d.IOM, module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")), fabric)
		e.State(m.iomOperability, mo.Get("operability"), d.Chassis, d.IOM)
		e.State(m.iomThermal, mo.Get("thermal"), d.Chassis, d.IOM)
		e.Bool(m.iomOK, mo.Get("operability") == "operable", d.Chassis, d.IOM)
		e.Attr(m.iomActive, prometheus.GaugeValue, mo, "numOfActiveFabricPorts", 0, d.Chassis, d.IOM)
	}
	for _, mo := range s.Class("equipmentIOCardStats") {
		d := dn.Parse(mo.DN)
		if d.Chassis == "" || d.IOM == "" {
			continue
		}
		emitSensors(e, m.iomTemp, mo, iomSensors, d.Chassis, d.IOM)
		m.iomStats.Emit(e, mo, d.Chassis, d.IOM)
	}
	for _, mo := range s.Class("equipmentFex") {
		d := dn.Parse(mo.DN)
		if d.Fex == "" {
			e.Drop("unknown_dn")
			continue
		}
		fabric, ok := fabricID(mo.Get("switchId"))
		if !ok {
			fabric = "unknown"
		}
		e.Info(m.fexInfo, d.Fex, module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")), fabric)
		e.State(m.fexOperability, mo.Get("operability"), d.Fex)
		e.Bool(m.fexOK, mo.Get("operability") == "operable", d.Fex)
	}
}

func (m *hardware) collectFI(s *module.Snapshot, e *module.Emitter) {
	for _, mo := range s.Class("networkElement") {
		d := dn.Parse(mo.DN)
		if d.Switch == "" {
			e.Drop("unknown_dn")
			continue
		}
		e.Info(m.fiInfo, d.Switch, module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")))
		e.State(m.fiOperability, mo.Get("operability"), d.Switch)
		e.State(m.fiThermal, mo.Get("thermal"), d.Switch)
		e.Bool(m.fiOK, mo.Get("operability") == "operable", d.Switch)
		if v := mo.Get("operEvacState"); v != "" {
			e.State(m.fiEvac, v, d.Switch)
		}
		if v, ok := positive(mo.Get("totalMemory")); ok {
			e.Gauge(m.fiMemory, v*module.MiB, d.Switch)
		}
	}
	for class, sensors := range map[string][]sensor{"swEnvStats": fiSensors, "swCardEnvStats": fiCardSensors} {
		for _, mo := range s.Class(class) {
			d := dn.Parse(mo.DN)
			if d.Switch == "" {
				e.Drop("unknown_dn")
				continue
			}
			emitSensors(e, m.fiTemp, mo, sensors, d.Switch)
		}
	}
	for _, mo := range s.Class("mgmtEntity") {
		d := dn.Parse(mo.DN)
		f := d.MgmtEntity
		if f == "" {
			e.Drop("unknown_dn")
			continue
		}
		e.State(m.mgmtLeader, mo.Get("leadership"), f)
		e.State(m.mgmtState, mo.Get("state"), f)
		e.State(m.mgmtHAReadiness, mo.Get("haReadiness"), f)
		e.State(m.mgmtServices, mo.Get("mgmtServicesState"), f)
		e.State(m.mgmtUmbilical, mo.Get("umbilicalState"), f)
		e.Bool(m.mgmtHAReady, yes(mo.Get("haReady")), f)
		e.Bool(m.mgmtVersionMismatch, yes(mo.Get("versionMismatch")), f)
		e.Bool(m.mgmtUp, mo.Get("state") == "up", f)
	}
}

func (m *hardware) collectPSUs(s *module.Snapshot, e *module.Emitter) {
	for _, mo := range s.Class("equipmentPsu") {
		d := dn.Parse(mo.DN)
		kind, lv, ok := containerOf(&d)
		if !ok || d.PSU == "" || m.psuOK[kind] == nil {
			e.Drop("unknown_dn")
			continue
		}
		lv = append(lv, d.PSU)
		if equipped(mo.Get("presence")) {
			e.Info(m.psuInfo[kind], append(slices.Clone(lv), module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")))...)
		}
		e.State(m.psuOperability[kind], mo.Get("operability"), lv...)
		e.State(m.psuPower[kind], mo.Get("power"), lv...)
		e.State(m.psuThermal[kind], mo.Get("thermal"), lv...)
		e.State(m.psuVoltage[kind], mo.Get("voltage"), lv...)
		e.State(m.psuPresence[kind], mo.Get("presence"), lv...)
		e.Bool(m.psuOK[kind], mo.Get("operability") == "operable", lv...)
		if v, ok := positive(mo.Get("psuWattage")); ok {
			e.Gauge(m.psuCapacity[kind], v, lv...)
		}
	}
	for _, mo := range s.Class("equipmentPsuStats") {
		d := dn.Parse(mo.DN)
		kind, lv, ok := containerOf(&d)
		if !ok || d.PSU == "" || m.psuOK[kind] == nil {
			e.Drop("unknown_dn")
			continue
		}
		lv = append(lv, d.PSU)
		e.Attr(m.psuInPower[kind], prometheus.GaugeValue, mo, "inputPower", 0, lv...)
		e.Attr(m.psuInVoltage[kind], prometheus.GaugeValue, mo, "input210v", 0, lv...)
		e.Attr(m.psuOutPower[kind], prometheus.GaugeValue, mo, "outputPower", 0, lv...)
		e.Attr(m.psuOutCurrent[kind], prometheus.GaugeValue, mo, "outputCurrent", 0, lv...)
		e.Attr(m.psuOutVoltage[kind], prometheus.GaugeValue, mo, "output12v", 0, slices.Concat(lv, []string{"12v"})...)
		e.Attr(m.psuOutVoltage[kind], prometheus.GaugeValue, mo, "output3v3", 0, slices.Concat(lv, []string{"3v3"})...)
		e.Attr(m.psuI2C[kind], prometheus.CounterValue, mo, "PsuI2CErrors", 0, lv...)
		emitSensors(e, m.psuTemp[kind], mo, psuSensors, lv...)
	}
}

func (m *hardware) collectFans(s *module.Snapshot, e *module.Emitter) {
	for _, mo := range s.Class("equipmentFan") {
		d := dn.Parse(mo.DN)
		kind, lv, ok := containerOf(&d)
		if !ok || d.Fan == "" {
			e.Drop("unknown_dn")
			continue
		}
		lv = append(lv, fanID(&d))
		e.State(m.fanOperability[kind], mo.Get("operability"), lv...)
		e.Bool(m.fanOK[kind], mo.Get("operability") == "operable", lv...)
	}
	for _, mo := range s.Class("equipmentFanModule") {
		d := dn.Parse(mo.DN)
		kind, lv, ok := containerOf(&d)
		if !ok || d.FanModule == "" || m.fmOK[kind] == nil {
			e.Drop("unknown_dn")
			continue
		}
		lv = append(lv, d.FanModule)
		e.State(m.fmOperability[kind], mo.Get("operability"), lv...)
		e.Bool(m.fmOK[kind], mo.Get("operability") == "operable", lv...)
	}
	for _, mo := range s.Class("equipmentFanModuleStats") {
		d := dn.Parse(mo.DN)
		kind, lv, ok := containerOf(&d)
		if !ok || d.FanModule == "" || m.fmTemp[kind] == nil {
			e.Drop("unknown_dn")
			continue
		}
		if v, ok := positive(mo.Get("ambientTemp")); ok {
			e.Gauge(m.fmTemp[kind], v, append(lv, d.FanModule)...)
		}
	}
}

// firmwareComponent names the component a firmwareRunning object belongs
// to: its parent DN without "sys/" and a trailing "/mgmt" or "/bios", or
// "ucsm" for UCS Manager itself.
func firmwareComponent(fwDN string) string {
	c := strings.TrimPrefix(ucsm.ParentDN(fwDN), "sys/")
	for _, suffix := range []string{"/mgmt", "/bios"} {
		c = strings.TrimSuffix(c, suffix)
	}
	if c == "mgmt" || c == "sys" || c == "" {
		return "ucsm"
	}
	return c
}
