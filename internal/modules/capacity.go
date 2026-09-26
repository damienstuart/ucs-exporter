// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Derived from ucs/computecapacity.py of prometheus-ucs-exporter,
// (c) 2022 Marshall Wace, GPL-3.0-only.

package modules

import (
	"sort"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
)

var serverClasses = map[string]string{"computeBlade": "blade", "computeRackUnit": "rack"}

type capacity struct {
	base
	info, sp, slots *prometheus.Desc
	servers         *module.Table
}

func newCapacity() *capacity {
	m := &capacity{base: base{name: "capacity", description: "Server inventory and capacity: CPUs, cores, memory, blade slots (computeBlade, computeRackUnit, fabricComputeSlotEp)."}}
	m.info = m.desc("ucs_server_info", "Server information (UCSM computeBlade, computeRackUnit).", labelServer, "type", "model", "serial", "vendor")
	m.sp = m.desc("ucs_server_service_profile_info", "Service profile associated with a server (UCSM computeBlade/computeRackUnit.assignedToDn).", labelServer, labelOrg, labelServiceProfile)
	m.slots = m.desc("ucs_chassis_blade_slots", "Blade slots of a chassis by presence (UCSM fabricComputeSlotEp.presence).", labelChassis, "presence")
	rows := []module.Row{
		module.G("numOfCpus", "cpus", "Number of CPU sockets populated"),
		module.G("numOfCores", "cpu_cores", "Number of CPU cores"),
		module.G("numOfCoresEnabled", "cpu_cores_enabled", "Number of enabled CPU cores"),
		module.G("numOfThreads", "cpu_threads", "Number of CPU threads"),
		module.G("numOfAdaptors", "adaptors", "Number of adapters"),
		module.G("totalMemory", "memory_bytes", "Installed memory in bytes").Scaled(module.MiB),
		module.G("availableMemory", "memory_available_bytes", "Available memory in bytes").Scaled(module.MiB),
	}
	for class := range serverClasses {
		m.query(class, "model", "serial", "vendor", "assignedToDn")
	}
	m.servers = m.sharedTable([]string{"computeBlade", "computeRackUnit"}, containerPrefixServer, []string{labelServer}, rows...)
	m.query("fabricComputeSlotEp", "presence")
	return m
}

func (m *capacity) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, class := range []string{"computeBlade", "computeRackUnit"} {
		for _, mo := range s.Class(class) {
			d := dn.Parse(mo.DN)
			server, ok := d.Server()
			if !ok {
				e.Drop("unknown_dn")
				continue
			}
			e.Info(m.info, server, serverClasses[class], module.OrUnknown(mo.Get("model")), module.OrUnknown(mo.Get("serial")), module.OrUnknown(mo.Get("vendor")))
			m.servers.Emit(e, mo, server)
			if org, sp, ok := spOf(mo.Get("assignedToDn")); ok {
				e.Info(m.sp, server, org, sp)
			}
		}
	}

	type key struct{ chassis, presence string }
	counts := map[key]int{}
	for _, mo := range s.Class("fabricComputeSlotEp") {
		d := dn.Parse(mo.DN)
		if d.ServerChassis == "" {
			e.Drop("unknown_dn")
			continue
		}
		counts[key{d.ServerChassis, module.OrUnknown(mo.Get("presence"))}]++
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].chassis+keys[i].presence < keys[j].chassis+keys[j].presence })
	for _, k := range keys {
		e.Gauge(m.slots, float64(counts[k]), k.chassis, k.presence)
	}
	return nil
}

// spOf parses a service profile DN into its org path and name.
func spOf(spDN string) (org, sp string, ok bool) {
	if spDN == "" {
		return "", "", false
	}
	d := dn.Parse(spDN)
	if d.SP == "" || len(d.Orgs) == 0 {
		return "", "", false
	}
	return d.OrgPath(), d.SP, true
}
