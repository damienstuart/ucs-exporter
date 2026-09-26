// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.yaml.in/yaml/v2"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/poller"
)

func TestMetricsDocUpToDate(t *testing.T) {
	var buf bytes.Buffer
	if err := runModules(&buf, true); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../docs/metrics.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Error("docs/metrics.md is out of date; run make docs")
	}
}

// The example configurations must only use known keys. (They reference
// files that do not exist here, so full validation is not possible.)
func TestExampleConfigKeys(t *testing.T) {
	for _, f := range []string{"../../examples/config.yml", "../../examples/compose/config.yml"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var cfg config.Config
		if err := yaml.UnmarshalStrict(b, &cfg); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		if len(cfg.Domains) == 0 {
			t.Errorf("%s: no domains", f)
		}
	}
}

// Every ucs_* metric used by the Grafana dashboard must exist.
func TestDashboardMetrics(t *testing.T) {
	known := map[string]bool{}
	for _, m := range allModules() {
		for _, info := range module.Metrics(m) {
			known[info.Name] = true
		}
	}
	cfg, err := config.Parse([]byte("domains: [{name: x, username: u, password: p}]"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := cfg.Lookup("x")
	p, err := poller.New(poller.Options{Config: r})
	if err != nil {
		t.Fatal(err)
	}
	st := &poller.State{Result: poller.ResultSuccess, LastSuccess: time.Now(),
		Classes: map[string]poller.ClassStatus{"c": {LastSuccess: time.Now()}},
		Modules: map[string]module.ModuleStatus{"m": {Dropped: map[string]int{"r": 1}}}}
	ch := make(chan prometheus.Metric, 1000)
	p.Health(st).Collect(ch)
	close(ch)
	for m := range ch {
		if info, ok := module.DescInfo(m.Desc()); ok {
			known[info.Name] = true
		}
	}

	b, err := os.ReadFile("../../grafana/dashboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var dash struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Definition string `json:"definition"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(b, &dash); err != nil {
		t.Fatal(err)
	}
	names := regexp.MustCompile(`\bucs_[a-z0-9_]+`)
	check := func(where, expr string) {
		for _, n := range names.FindAllString(expr, -1) {
			if !known[n] {
				t.Errorf("%s: unknown metric %s", where, n)
			}
		}
	}
	for _, p := range dash.Panels {
		for _, tg := range p.Targets {
			check("panel "+p.Title, tg.Expr)
		}
	}
	for _, v := range dash.Templating.List {
		check("variable", v.Definition)
	}
}
