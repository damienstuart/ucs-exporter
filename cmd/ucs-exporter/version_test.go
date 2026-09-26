// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"runtime/debug"
	"testing"

	"github.com/prometheus/common/version"
)

func TestApplyBuildInfo(t *testing.T) {
	saved := [2]string{version.Version, version.BuildDate}
	defer func() { version.Version, version.BuildDate = saved[0], saved[1] }()

	cases := []struct {
		name, ldVersion, ldDate, module, vcsTime string
		wantVersion, wantDate                    string
	}{
		{"go install tag", "", "", "v0.1.0", "", "0.1.0", ""},
		{"checkout build", "", "", "v0.1.1-0.20261001120000-abcdef123456+dirty", "2026-10-01T12:00:00Z",
			"0.1.1-0.20261001120000-abcdef123456+dirty", "2026-10-01T12:00:00Z"},
		{"devel", "", "", "(devel)", "", "", ""},
		{"ldflags win", "0.1.0", "20261001-12:00:00", "v9.9.9", "2030-01-01T00:00:00Z", "0.1.0", "20261001-12:00:00"},
	}
	for _, c := range cases {
		version.Version, version.BuildDate = c.ldVersion, c.ldDate
		bi := &debug.BuildInfo{Main: debug.Module{Version: c.module}}
		if c.vcsTime != "" {
			bi.Settings = []debug.BuildSetting{{Key: "vcs.time", Value: c.vcsTime}}
		}
		applyBuildInfo(bi)
		if version.Version != c.wantVersion || version.BuildDate != c.wantDate {
			t.Errorf("%s: version=%q date=%q; want %q %q", c.name, version.Version, version.BuildDate, c.wantVersion, c.wantDate)
		}
	}
	applyBuildInfo(nil) // must not panic
}
