// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package module

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// MetricInfo describes a metric for documentation.
type MetricInfo struct {
	Name   string
	Help   string
	Type   string // counter or gauge, inferred from the name
	Labels []string
}

var descRE = regexp.MustCompile(`^Desc\{fqName: ("(?:[^"\\]|\\.)*"), help: ("(?:[^"\\]|\\.)*"), .*variableLabels: \{([^}]*)\}\}$`)

// DescInfo extracts the name, help and variable labels of a descriptor.
// prometheus.Desc has no accessors, so this parses Desc.String().
func DescInfo(d *prometheus.Desc) (MetricInfo, bool) {
	m := descRE.FindStringSubmatch(d.String())
	if m == nil {
		return MetricInfo{}, false
	}
	name, err1 := strconv.Unquote(m[1])
	help, err2 := strconv.Unquote(m[2])
	if err1 != nil || err2 != nil {
		return MetricInfo{}, false
	}
	info := MetricInfo{Name: name, Help: help, Type: "gauge"}
	if strings.HasSuffix(name, "_total") {
		info.Type = "counter"
	}
	if m[3] != "" {
		info.Labels = strings.Split(m[3], ",")
	}
	return info, true
}

// Metrics lists the metrics a module can emit, sorted by name.
func Metrics(m Module) []MetricInfo {
	ch := make(chan *prometheus.Desc)
	go func() { m.Describe(ch); close(ch) }()
	var out []MetricInfo
	for d := range ch {
		if info, ok := DescInfo(d); ok {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
