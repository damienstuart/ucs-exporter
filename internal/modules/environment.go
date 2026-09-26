// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Derived from ucs/power.py, ucs/temperature.py, ucs/fan.py and
// ucs/swsystem.py of prometheus-ucs-exporter, (c) 2022 Marshall Wace,
// GPL-3.0-only.

package modules

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// power: server motherboard power.
type power struct {
	base
	t *module.Table
}

func newPower() *power {
	m := &power{base: base{name: "power", description: "Server power consumption, input current and voltage (computeMbPowerStats)."}}
	m.t = m.table("computeMbPowerStats", containerPrefixServer, []string{labelServer},
		module.G("consumedPower", "power_watts", "Power consumed by the server motherboard in watts"),
		module.G("inputCurrent", "input_current_amperes", "Motherboard input current in amperes"),
		module.G("inputVoltage", "input_voltage_volts", "Motherboard input voltage in volts"),
	)
	return m
}

func (m *power) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, mo := range s.Class("computeMbPowerStats") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || !d.Board {
			e.Drop("unknown_dn")
			continue
		}
		m.t.Emit(e, mo, server)
	}
	return nil
}

// cpuTemperature: processor temperatures.
type cpuTemperature struct {
	base
	t *module.Table
}

func newCPUTemperature() *cpuTemperature {
	m := &cpuTemperature{base: base{name: "cpu_temperature", description: "Server CPU temperatures (processorEnvStats)."}}
	m.t = m.table("processorEnvStats", "ucs_server_cpu", []string{labelServer, "cpu"},
		module.G("temperature", "temperature_celsius", "CPU temperature in degrees Celsius"),
	)
	return m
}

func (m *cpuTemperature) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, mo := range s.Class("processorEnvStats") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || d.CPU == "" {
			e.Drop("unknown_dn")
			continue
		}
		m.t.Emit(e, mo, server, d.CPU)
	}
	return nil
}

// fans: fan speeds of chassis, rack servers, FEXes, IOMs and fabric
// interconnects.
type fans struct {
	base
	speed map[string]*prometheus.Desc
	drive *prometheus.Desc
}

func newFans() *fans {
	m := &fans{base: base{name: "fans", description: "Fan speeds (equipmentFanStats, equipmentNetworkElementFanStats)."}}
	m.speed = m.containerDescs(allContainers, "fan_speed_rpm", "Fan speed in revolutions per minute (UCSM equipmentFanStats.speed, equipmentNetworkElementFanStats.speed).", "fan")
	m.drive = m.desc("ucs_fi_fan_drive_ratio", "Fabric interconnect fan drive level, 0-1 (UCSM equipmentNetworkElementFanStats.drivePercentage / 100).", labelFabric, "fan")
	m.query("equipmentFanStats", "speed")
	m.query("equipmentNetworkElementFanStats", "speed", "drivePercentage")
	return m
}

func (m *fans) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, class := range []string{"equipmentFanStats", "equipmentNetworkElementFanStats"} {
		for _, mo := range s.Class(class) {
			d := dn.Parse(mo.DN)
			kind, lv, ok := containerOf(&d)
			if !ok || d.Fan == "" {
				e.Drop("unknown_dn")
				continue
			}
			fan := fanID(&d)
			if v, ok := positiveOrZero(mo.Get("speed")); ok {
				e.Gauge(m.speed[kind], v, append(lv, fan)...)
			}
			if kind == dn.ContainerFI {
				if v, ok := mo.Float("drivePercentage"); ok {
					e.Gauge(m.drive, v/100, d.Switch, fan)
				}
			}
		}
	}
	return nil
}

// positiveOrZero parses a non-negative number.
func positiveOrZero(s string) (float64, bool) {
	v, ok := ucsm.ParseNumber(s)
	return v, ok && v >= 0
}

// fiSystem: fabric interconnect CPU and memory.
type fiSystem struct {
	base
	t *module.Table
}

func newFISystem() *fiSystem {
	m := &fiSystem{base: base{name: "fi_system", description: "Fabric interconnect load, memory and uptime (swSystemStats)."}}
	m.t = m.table("swSystemStats", "ucs_fi", []string{labelFabric},
		module.G("load", "load", "Fabric interconnect CPU load average"),
		module.G("memAvailable", "memory_available_bytes", "Fabric interconnect memory available in bytes").Scaled(module.MiB),
		module.G("memCached", "memory_cached_bytes", "Fabric interconnect memory cached in bytes").Scaled(module.MiB),
		module.G("kernelMemTotal", "kernel_memory_total_bytes", "Fabric interconnect kernel memory total in bytes").Scaled(1024),
		module.G("kernelMemFree", "kernel_memory_free_bytes", "Fabric interconnect kernel memory free in bytes").Scaled(1024),
		module.Row{Attr: "upTime", Suffix: "uptime_seconds", Help: "Fabric interconnect uptime in seconds, reported by UCSM 4.3(4a) and later", Type: prometheus.GaugeValue, Parse: parseUptime},
		module.C("CorrectableParityError", "correctable_parity_errors_total", "Correctable parity errors"),
	)
	return m
}

func (m *fiSystem) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, mo := range s.Class("swSystemStats") {
		d := dn.Parse(mo.DN)
		if d.Switch == "" {
			e.Drop("unknown_dn")
			continue
		}
		m.t.Emit(e, mo, d.Switch)
	}
	return nil
}
