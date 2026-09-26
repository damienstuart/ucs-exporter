// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package modules

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/module"
)

type system struct {
	base
	info, uptime *prometheus.Desc
}

func newSystem() *system {
	m := &system{base: base{name: "system", description: "UCS domain identity, version and uptime (topSystem, versionApplication)."}}
	m.info = m.desc("ucs_domain_info", "UCS domain information: system name, cluster mode and UCS Manager version (UCSM topSystem, versionApplication).", "system_name", "mode", "version")
	m.uptime = m.desc("ucs_system_uptime_seconds", "UCS Manager system uptime (UCSM topSystem.systemUpTime).")
	m.query("topSystem", "name", "mode", "systemUpTime")
	m.query("versionApplication", "version")
	return m
}

func (m *system) Collect(s *module.Snapshot, e *module.Emitter) error {
	top := s.Get("sys")
	if top == nil || top.Class != "topSystem" {
		return nil
	}
	version := ""
	if app := s.Get("sys/version/application"); app != nil {
		version = app.Get("version")
	}
	e.Info(m.info, module.OrUnknown(top.Get("name")), module.OrUnknown(top.Get("mode")), module.OrUnknown(version))
	if v, ok := parseUptime(top.Get("systemUpTime")); ok {
		e.Gauge(m.uptime, v)
	}
	return nil
}
