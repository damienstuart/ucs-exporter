// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"runtime/debug"
	"strings"

	"github.com/prometheus/common/version"
)

// applyBuildInfo fills in version details that were not set at link time
// from the Go build information. "make build" and the Dockerfile set them
// with -ldflags; binaries built with "go install ...@v0.1.0" only carry the
// module version, and binaries built in a checkout also carry VCS details.
func applyBuildInfo(bi *debug.BuildInfo) {
	if bi == nil {
		return
	}
	if version.Version == "" {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			version.Version = strings.TrimPrefix(v, "v")
		}
	}
	if version.BuildDate == "" {
		for _, s := range bi.Settings {
			if s.Key == "vcs.time" {
				version.BuildDate = s.Value
			}
		}
	}
}
