// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Derived from ucs/memerror.py and ucs/faults.py of prometheus-ucs-exporter,
// (c) 2022 Marshall Wace, GPL-3.0-only.

package modules

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/dn"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// memoryErrors: per-DIMM error counters.
type memoryErrors struct {
	base
	t *module.Table
}

func newMemoryErrors() *memoryErrors {
	m := &memoryErrors{base: base{name: "memory_errors", description: "Per-DIMM memory error counters (memoryErrorStats)."}}
	m.t = m.table("memoryErrorStats", "ucs_server_dimm", []string{labelServer, "memory_array", "dimm"},
		module.C("eccSinglebitErrors", "ecc_singlebit_errors_total", "Single-bit (corrected) ECC errors"),
		module.C("eccMultibitErrors", "ecc_multibit_errors_total", "Multi-bit (uncorrectable) ECC errors"),
		module.C("addressParityErrors", "address_parity_errors_total", "Address parity errors"),
		module.C("addressParityErrorsCorrectable", "address_parity_correctable_errors_total", "Correctable address parity errors"),
		module.C("addressParityErrorsUnCorrectable", "address_parity_uncorrectable_errors_total", "Uncorrectable address parity errors"),
		module.C("DramWriteDataCorrectableCRCErrors", "dram_write_crc_correctable_errors_total", "Correctable DRAM write data CRC errors"),
		module.C("DramWriteDataUnCorrectableCRCErrors", "dram_write_crc_uncorrectable_errors_total", "Uncorrectable DRAM write data CRC errors"),
		module.C("mismatchErrors", "mismatch_errors_total", "Memory mismatch errors"),
	)
	m.query("memoryUnit", "presence")
	return m
}

func (m *memoryErrors) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, mo := range s.Class("memoryErrorStats") {
		d := dn.Parse(mo.DN)
		server, ok := d.Server()
		if !ok || d.MemArray == "" || d.DIMM == "" {
			e.Drop("unknown_dn")
			continue
		}
		// Skip empty DIMM slots when their presence is known.
		if unit := s.Parent(mo); unit != nil && !equipped(unit.Get("presence")) {
			continue
		}
		m.t.Emit(e, mo, server, d.MemArray, d.DIMM)
	}
	return nil
}

// Fault severities and types from the UCSM faultInst enumerations, used to
// export a stable set of series.
var (
	faultSeverities = []string{"critical", "major", "minor", "warning", "info", "condition"}
	faultTypes      = []string{
		"chassis-profile", "configuration", "connectivity", "environmental", "equipment", "forward", "fsm",
		"generic", "inventory", "management", "network", "operational", "policy", "power", "security",
		"security-configuration", "server", "sysdebug", "unmanageable-hardware",
	}
)

// faults: active fault counts.
type faults struct {
	base
	count *prometheus.Desc
}

func newFaults() *faults {
	m := &faults{base: base{name: "faults", description: "Active (not cleared) faults by severity and type (faultInst)."}}
	m.count = m.desc("ucs_faults", "Active UCSM faults (severity other than cleared) by severity and type (UCSM faultInst).", "severity", "type")
	m.queries = append(m.queries, module.Query{
		Class:  "faultInst",
		Filter: ucsm.Ne("faultInst", "severity", "cleared"),
		Attrs:  []string{"severity", "type"},
	})
	return m
}

func (m *faults) Collect(s *module.Snapshot, e *module.Emitter) error {
	type key struct{ severity, typ string }
	counts := map[key]int{}
	for _, sev := range faultSeverities {
		for _, typ := range faultTypes {
			counts[key{sev, typ}] = 0
		}
	}
	for _, mo := range s.Class("faultInst") {
		sev := mo.Get("severity")
		if sev == "cleared" || sev == "" {
			continue
		}
		counts[key{sev, module.OrUnknown(mo.Get("type"))}]++
	}
	for k, n := range counts {
		e.Gauge(m.count, float64(n), k.severity, k.typ)
	}
	return nil
}
