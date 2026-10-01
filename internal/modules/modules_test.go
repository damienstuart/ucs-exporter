// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package modules

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

var update = flag.Bool("update", false, "rewrite golden files")

// allOptions enables every optional feature so that tests cover them.
var allOptions = config.Options{VirtualVLANs: "full", IOMHostPorts: true, AdaptorUplinkErrors: true, FirmwareTypes: config.DefaultFirmwareTypes}

const repoRoot = "../.."

func TestSDKMetadata(t *testing.T) {
	var meta struct {
		Classes map[string]struct {
			Attrs map[string]any `json:"attrs"`
		} `json:"classes"`
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, "testdata/sdkmeta/classes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	for _, q := range module.MergeQueries(Builtin(allOptions)) {
		c, ok := meta.Classes[q.Class]
		if !ok {
			t.Errorf("class %s is not in testdata/sdkmeta/classes.json (typo, or regenerate with testdata/sdkmeta/generate.py)", q.Class)
			continue
		}
		for _, a := range q.Attrs {
			if _, ok := c.Attrs[a]; !ok {
				t.Errorf("class %s has no attribute %s", q.Class, a)
			}
		}
		// The poller relies on statistics classes, and only those, having
		// the suspect flag.
		if _, ok := c.Attrs[ucsm.SuspectAttr]; ok != ucsm.IsStatsClass(q.Class) {
			t.Errorf("class %s: has suspect attribute = %v, IsStatsClass = %v", q.Class, ok, ucsm.IsStatsClass(q.Class))
		}
	}
	// The zero-filled fault enumerations must match the SDK.
	for _, sev := range faultSeverities {
		if !slices.Contains(enum(t, meta.Classes["faultInst"].Attrs["severity"]), sev) {
			t.Errorf("unknown fault severity %s", sev)
		}
	}
	types := enum(t, meta.Classes["faultInst"].Attrs["type"])
	for _, typ := range faultTypes {
		if !slices.Contains(types, typ) {
			t.Errorf("unknown fault type %s", typ)
		}
	}
	if len(types) != len(faultTypes)+1 { // "any" is excluded
		t.Errorf("fault types changed: %v", types)
	}
}

func enum(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("not an enum: %v", v)
	}
	var out []string
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}

// describeAll registers each module as a collector in a pedantic registry,
// which rejects duplicate or inconsistent descriptors across modules.
func TestDescriptors(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	for _, m := range Builtin(allOptions) {
		if err := reg.Register(describer{m}); err != nil {
			t.Errorf("module %s: %v", m.Name(), err)
		}
		ch := make(chan *prometheus.Desc, 1000)
		m.Describe(ch)
		close(ch)
		n := 0
		for d := range ch {
			n++
			if s := d.String(); strings.Contains(s, `"domain"`) {
				t.Errorf("module %s declares the reserved domain label: %s", m.Name(), s)
			}
			if !strings.Contains(d.String(), `fqName: "ucs_`) {
				t.Errorf("module %s: metric without ucs_ prefix: %s", m.Name(), d)
			}
		}
		if n == 0 {
			t.Errorf("module %s describes no metrics", m.Name())
		}
		if m.Description() == "" || len(m.Queries()) == 0 {
			t.Errorf("module %s lacks a description or queries", m.Name())
		}
	}
}

type describer struct{ m module.Module }

func (d describer) Describe(ch chan<- *prometheus.Desc) { d.m.Describe(ch) }
func (d describer) Collect(chan<- prometheus.Metric)    {}

func TestBuild(t *testing.T) {
	all, err := Build(config.Resolved{Options: allOptions})
	if err != nil || len(all) != len(Names()) {
		t.Fatalf("Build(all) = %d modules, %v", len(all), err)
	}
	some, err := Build(config.Resolved{Modules: []string{"faults", "system", "faults"}})
	if err != nil || len(some) != 2 || some[0].Name() != "faults" {
		t.Errorf("Build(subset) = %v, %v", some, err)
	}
	if _, err := Build(config.Resolved{Modules: []string{"nope"}}); err == nil {
		t.Error("unknown module accepted")
	}
}

// loadFixtures decodes every <classId>.xml in dir, applying keep if non-nil.
func loadFixtures(t *testing.T, dir string, keep map[string]func(class, attr string) bool) map[string]*module.ClassData {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.xml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s: %v", dir, err)
	}
	out := map[string]*module.ClassData{}
	for _, f := range files {
		class := strings.TrimSuffix(filepath.Base(f), ".xml")
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var opt ucsm.DecodeOptions
		if keep != nil {
			opt.Keep = keep[class]
		}
		res, err := ucsm.Decode(bytes.NewReader(b), "configResolveClass", opt)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, mo := range res.Objects {
			if mo.Class != class {
				t.Fatalf("%s: object of class %s", f, mo.Class)
			}
		}
		out[class] = &module.ClassData{Objects: res.Objects}
	}
	return out
}

func render(t *testing.T, classes map[string]*module.ClassData, mods []module.Module) string {
	t.Helper()
	fams, status, err := module.Render(module.NewSnapshot("synthetic", classes), mods, prometheus.Labels{"domain": "synthetic"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for name, st := range status {
		if !st.OK() {
			t.Errorf("module %s failed: %s", name, st.Err)
		}
		if len(st.Errors) > 0 {
			t.Errorf("module %s errors: %v", name, st.Errors)
		}
	}
	var buf bytes.Buffer
	if err := module.WriteText(&buf, fams); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestGolden(t *testing.T) {
	scenarios, err := filepath.Glob(filepath.Join(repoRoot, "testdata/fixtures/*"))
	if err != nil {
		t.Fatal(err)
	}
	mods := Builtin(allOptions)
	queries := module.MergeQueries(mods)
	keep := map[string]func(string, string) bool{}
	for _, q := range queries {
		keep[q.Class] = q.KeepFunc()
	}
	for _, dir := range scenarios {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			full := loadFixtures(t, dir, nil)
			projected := loadFixtures(t, dir, keep)

			// Output must not depend on attribute projection: a difference
			// means a module reads an attribute it did not declare.
			if a, b := render(t, full, mods), render(t, projected, mods); a != b {
				t.Errorf("output differs with attribute projection; a module reads undeclared attributes")
			}
			if name == "synthetic" {
				for _, q := range queries {
					if _, ok := full[q.Class]; !ok {
						t.Errorf("no fixture for class %s", q.Class)
					}
				}
			}

			for _, m := range mods {
				out := render(t, projected, []module.Module{m})
				golden := filepath.Join(repoRoot, "testdata/golden", name, m.Name()+".prom")
				if *update {
					if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("%v (run go test ./internal/modules -update)", err)
				}
				if out != string(want) {
					t.Errorf("module %s output differs from %s (run go test ./internal/modules -update and review the diff)", m.Name(), golden)
				}
				if out == "" {
					t.Errorf("module %s produced no metrics for scenario %s", m.Name(), name)
				}
				lint(t, m.Name(), out)
			}
		})
	}
}

func lint(t *testing.T, name, text string) {
	t.Helper()
	problems, err := promlint.New(strings.NewReader(text)).Lint()
	if err != nil {
		t.Errorf("module %s: lint: %v", name, err)
	}
	for _, pr := range problems {
		t.Errorf("module %s: lint %s: %s", name, pr.Metric, pr.Text)
	}
}
