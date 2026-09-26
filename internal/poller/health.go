// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package poller

import (
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, append([]string{"domain"}, labels...), nil)
}

var (
	upDesc              = desc("ucs_up", "Whether the latest poll of the UCS domain succeeded at least partially (logged in and retrieved at least one class).")
	pollDurationDesc    = desc("ucs_poll_duration_seconds", "Duration of the latest poll.")
	pollTimestampDesc   = desc("ucs_poll_timestamp_seconds", "End time of the latest poll.")
	pollSuccessTSDesc   = desc("ucs_poll_last_success_timestamp_seconds", "End time of the latest successful or partial poll.")
	snapshotAgeDesc     = desc("ucs_snapshot_age_seconds", "Seconds since the latest successful or partial poll.")
	pollsDesc           = desc("ucs_polls_total", "Polls by result (success, partial, failure).", "result")
	overrunsDesc        = desc("ucs_poll_overruns_total", "Polls that took longer than the poll interval.")
	classDurationDesc   = desc("ucs_class_query_duration_seconds", "Duration of the latest query for a UCSM class.", "class")
	classSuccessDesc    = desc("ucs_class_query_success", "Whether the latest query for a UCSM class succeeded.", "class")
	classErrorsDesc     = desc("ucs_class_query_errors_total", "Failed queries for a UCSM class.", "class")
	classObjectsDesc    = desc("ucs_class_objects", "Objects of a UCSM class in the latest snapshot (including stale data carried forward).", "class")
	classStaleDesc      = desc("ucs_class_stale", "Whether the snapshot uses data from an earlier poll for a UCSM class because the latest query failed.", "class")
	classLastSuccessTSD = desc("ucs_class_last_success_timestamp_seconds", "Time a UCSM class was last retrieved successfully.", "class")
	moduleSuccessDesc   = desc("ucs_module_success", "Whether a metric module rendered without error in the latest poll.", "module")
	moduleSeriesDesc    = desc("ucs_module_series", "Series produced by a metric module in the latest poll.", "module")
	sessionOpsDesc      = desc("ucs_session_operations_total", "UCSM session operations by type (login, refresh, reauth, logout) and result.", "op", "result")
	skippedDesc         = desc("ucs_module_skipped_objects", "Objects or series a metric module skipped in the latest poll because it could not identify them, by reason (unknown_dn, empty_label, invalid, ...).", "module", "reason")
)

// Health returns a collector for the poller's health metrics, reporting the
// given state.
func (p *Poller) Health(st *State) prometheus.Collector { return health{p, st} }

type health struct {
	p  *Poller
	st *State
}

func (h health) Describe(ch chan<- *prometheus.Desc) { prometheus.DescribeByCollect(h, ch) }

func (h health) Collect(ch chan<- prometheus.Metric) {
	d := h.p.cfg.Name
	g := func(desc *prometheus.Desc, v float64, lv ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, append([]string{d}, lv...)...)
	}
	c := func(desc *prometheus.Desc, v float64, lv ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, v, append([]string{d}, lv...)...)
	}
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	ts := func(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

	if st := h.st; st != nil {
		g(upDesc, b(st.Up()))
		g(pollDurationDesc, st.PollEnd.Sub(st.PollStart).Seconds())
		g(pollTimestampDesc, ts(st.PollEnd))
		if !st.LastSuccess.IsZero() {
			g(pollSuccessTSDesc, ts(st.LastSuccess))
			g(snapshotAgeDesc, time.Since(st.LastSuccess).Seconds())
		}
		for _, class := range sortedKeys(st.Classes) {
			cs := st.Classes[class]
			g(classDurationDesc, cs.Duration.Seconds(), class)
			g(classSuccessDesc, b(cs.Err == nil), class)
			g(classObjectsDesc, float64(cs.Objects), class)
			g(classStaleDesc, b(cs.Stale), class)
			if !cs.LastSuccess.IsZero() {
				g(classLastSuccessTSD, ts(cs.LastSuccess), class)
			}
		}
		for _, name := range sortedKeys(st.Modules) {
			ms := st.Modules[name]
			g(moduleSuccessDesc, b(ms.OK()), name)
			g(moduleSeriesDesc, float64(ms.Series), name)
			for _, reason := range sortedKeys(ms.Dropped) {
				g(skippedDesc, float64(ms.Dropped[reason]), name, reason)
			}
		}
	}

	p := h.p
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range []string{ResultSuccess, ResultPartial, ResultFailure} {
		c(pollsDesc, p.polls[r], r)
	}
	c(overrunsDesc, p.overruns)
	for _, q := range p.queries {
		c(classErrorsDesc, p.classErrors[q.Class], q.Class)
	}
	for _, op := range []string{"login", "refresh", "reauth", "logout"} {
		for _, r := range []string{"success", "failure"} {
			c(sessionOpsDesc, p.sessionOps[[2]string{op, r}], op, r)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
