// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/common/version"
	"go.yaml.in/yaml/v2"

	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
	"github.com/damienstuart/ucs-exporter/internal/ucsm/ucsmtest"
)

type captureOptions struct {
	cmd         *kingpin.CmdClause
	out         *string
	modules     *[]string
	extra       *[]string
	redact      *bool
	redactNames *bool
	keyFile     *string
	force       *bool
}

func addCapture(parent *kingpin.CmdClause) *captureOptions {
	c := &captureOptions{cmd: parent.Command("capture", "Save the response for every class the modules query as test fixtures (<class>.xml plus manifest.yaml).")}
	c.out = c.cmd.Flag("out", "Output directory, e.g. testdata/fixtures/site1.").Required().String()
	c.modules = c.cmd.Flag("module", "Capture only the classes of these modules (repeatable; default all).").Strings()
	c.extra = c.cmd.Flag("extra-class", "Also capture this class (repeatable).").Strings()
	c.redact = c.cmd.Flag("redact", "Pseudonymize serial numbers, UUIDs, MAC/WWN/IP addresses, asset tags and user labels.").Default("true").Bool()
	c.redactNames = c.cmd.Flag("redact-names", "Also pseudonymize organization and service profile names.").Bool()
	c.keyFile = c.cmd.Flag("redact-key-file", "File holding the pseudonymization key, so repeated captures map values identically (created if missing).").String()
	c.force = c.cmd.Flag("force", "Write into a non-empty directory.").Bool()
	return c
}

type manifestClass struct {
	Class    string `yaml:"class"`
	Filter   string `yaml:"filter,omitempty"`
	Objects  int    `yaml:"objects"`
	Duration string `yaml:"duration"`
	Error    string `yaml:"error,omitempty"`
}

type manifest struct {
	CapturedAt      time.Time       `yaml:"captured_at"`
	ExporterVersion string          `yaml:"exporter_version"`
	UCSMVersion     string          `yaml:"ucsm_version"`
	Domain          string          `yaml:"domain,omitempty"`
	Redacted        bool            `yaml:"redacted"`
	RedactedNames   bool            `yaml:"redacted_names"`
	Classes         []manifestClass `yaml:"classes"`
}

func (c *captureOptions) run(ctx context.Context, sess *ucsm.Session, domain string, timeout time.Duration) error {
	queries, err := c.queries()
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(*c.out); err == nil && len(entries) > 0 && !*c.force {
		return fmt.Errorf("%s is not empty (use --force)", *c.out)
	}
	if err := os.MkdirAll(*c.out, 0o750); err != nil {
		return err
	}
	var red *redactor
	if *c.redact {
		key, err := redactKey(*c.keyFile)
		if err != nil {
			return err
		}
		red = newRedactor(key, *c.redactNames)
	}

	m := manifest{
		CapturedAt:      time.Now().UTC().Truncate(time.Second),
		ExporterVersion: version.Version,
		UCSMVersion:     sess.Info().Version,
		Redacted:        *c.redact,
		RedactedNames:   *c.redact && *c.redactNames,
	}
	if !*c.redact {
		m.Domain = domain
	}
	for _, q := range queries {
		mc := manifestClass{Class: q.Class}
		if q.Filter != nil {
			mc.Filter = q.Filter.String()
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		raw, err := sess.Raw(rctx, func(cookie string) ucsm.Request { return ucsm.ResolveClassRequest(cookie, q.Class, q.Filter, false) })
		cancel()
		mc.Duration = time.Since(start).Truncate(time.Millisecond).String()
		if err == nil {
			var out []byte
			out, mc.Objects, err = c.render(raw, q.Class, red)
			if err == nil {
				err = writeFile(filepath.Join(*c.out, q.Class+".xml"), out)
			}
		}
		if err != nil {
			mc.Error = err.Error()
			fmt.Fprintf(os.Stderr, "%-36s error: %v\n", q.Class, err)
		} else {
			fmt.Fprintf(os.Stderr, "%-36s %6d objects  %s\n", q.Class, mc.Objects, mc.Duration)
		}
		m.Classes = append(m.Classes, mc)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(*c.out, "manifest.yaml"), b); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %d classes to %s; review the files before committing them\n", len(m.Classes), *c.out)
	return nil
}

// queries returns the classes to capture: those of the selected modules
// (with the poller's filters) plus any extra classes.
func (c *captureOptions) queries() ([]module.Query, error) {
	mods := allModules()
	if len(*c.modules) > 0 {
		var sel []module.Module
		for _, name := range *c.modules {
			i := slices.IndexFunc(mods, func(m module.Module) bool { return m.Name() == name })
			if i < 0 {
				return nil, fmt.Errorf("unknown module %q", name)
			}
			sel = append(sel, mods[i])
		}
		mods = sel
	}
	qs := module.MergeQueries(mods)
	for _, class := range *c.extra {
		if !slices.ContainsFunc(qs, func(q module.Query) bool { return q.Class == class }) {
			qs = append(qs, module.Query{Class: class})
		}
	}
	sort.Slice(qs, func(i, j int) bool { return qs[i].Class < qs[j].Class })
	return qs, nil
}

// render converts a raw response into the fixture format. Without a
// redactor the response is kept verbatim except for the session cookie.
func (c *captureOptions) render(raw []byte, class string, red *redactor) ([]byte, int, error) {
	res, err := ucsm.Decode(bytes.NewReader(raw), "configResolveClass", ucsm.DecodeOptions{})
	if err != nil {
		return nil, 0, err
	}
	if red == nil {
		return ucsm.Redact(raw), len(res.Objects), nil
	}
	for _, mo := range res.Objects {
		red.mo(mo)
	}
	var buf bytes.Buffer
	attrs := []xml.Attr{
		{Name: xml.Name{Local: "cookie"}, Value: ""},
		{Name: xml.Name{Local: "response"}, Value: "yes"},
		{Name: xml.Name{Local: "classId"}, Value: class},
	}
	if err := ucsmtest.EncodeResponse(&buf, "configResolveClass", attrs, "outConfigs", res.Objects); err != nil {
		return nil, 0, err
	}
	// One object per line keeps fixtures reviewable.
	out := strings.ReplaceAll(buf.String(), "></"+class+">", "/>\n")
	out = strings.Replace(out, "<outConfigs>", "\n<outConfigs>\n", 1)
	out = strings.Replace(out, "</outConfigs>", "</outConfigs>\n", 1)
	return []byte(out + "\n"), len(res.Objects), nil
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
