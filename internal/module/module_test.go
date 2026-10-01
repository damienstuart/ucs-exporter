// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package module

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

type toy struct {
	name    string
	queries []Query
	collect func(s *Snapshot, e *Emitter) error
	descs   []*prometheus.Desc
}

func (t toy) Name() string                          { return t.name }
func (t toy) Description() string                   { return "toy" }
func (t toy) Queries() []Query                      { return t.queries }
func (t toy) Collect(s *Snapshot, e *Emitter) error { return t.collect(s, e) }
func (t toy) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range t.descs {
		ch <- d
	}
}

func snap() *Snapshot {
	return NewSnapshot("ucs1", map[string]*ClassData{
		"computeBlade": {Objects: []*ucsm.MO{
			ucsm.NewMO("computeBlade", "sys/chassis-1/blade-1", "numOfCpus", "2", "model", "B200"),
			ucsm.NewMO("computeBlade", "sys/chassis-1/blade-2", "numOfCpus", "not-applicable", "model", "B200"),
		}},
		"adaptorUnit": {Objects: []*ucsm.MO{
			ucsm.NewMO("adaptorUnit", "sys/chassis-1/blade-1/adaptor-1", "model", "VIC1440"),
		}},
		"adaptorHostEthIf": {Objects: []*ucsm.MO{
			ucsm.NewMO("adaptorHostEthIf", "sys/chassis-1/blade-1/adaptor-1/host-eth-1"),
			ucsm.NewMO("adaptorHostEthIf", "sys/chassis-1/blade-1/adaptor-1/host-eth-2"),
		}},
	})
}

func TestSnapshotLookups(t *testing.T) {
	s := snap()
	if !s.Has("computeBlade") || s.Has("lsServer") || len(s.Class("lsServer")) != 0 {
		t.Error("Has/Class mismatch")
	}
	eth := s.Get("sys/chassis-1/blade-1/adaptor-1/host-eth-2")
	if eth == nil || s.Parent(eth).Class != "adaptorUnit" {
		t.Fatalf("Get/Parent = %v", eth)
	}
	if b := s.Ancestor(eth.DN, "computeBlade"); b == nil || b.DN != "sys/chassis-1/blade-1" {
		t.Errorf("Ancestor = %v", b)
	}
	if n := len(s.ChildrenOfClass("sys/chassis-1/blade-1/adaptor-1", "adaptorHostEthIf")); n != 2 {
		t.Errorf("children = %d", n)
	}
	if n := len(s.IndexBy("computeBlade", "model")["B200"]); n != 2 {
		t.Errorf("IndexBy = %d", n)
	}
}

func TestRender(t *testing.T) {
	cpus := NewTable("computeBlade", "ucs_server", []string{"server"}, G("numOfCpus", "cpus", "CPUs"))
	info := NewDesc("ucs_server_info", "Server info.", "server", "model")
	good := toy{name: "good", collect: func(s *Snapshot, e *Emitter) error {
		for _, mo := range s.Class("computeBlade") {
			server := strings.TrimPrefix(mo.DN, "sys/")
			cpus.Emit(e, mo, server)
			e.Info(info, server, mo.Get("model"))
		}
		e.Info(info, "", "x") // dropped: empty identity label
		return nil
	}}
	boom := toy{name: "boom", collect: func(s *Snapshot, e *Emitter) error {
		e.Info(info, "partial", "x")
		panic("kaboom")
	}}
	failing := toy{name: "failing", collect: func(s *Snapshot, e *Emitter) error {
		return errors.New("broken")
	}}
	fams, status, err := Render(snap(), []Module{good, boom, failing}, prometheus.Labels{"domain": "ucs1"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteText(&buf, fams); err != nil {
		t.Fatal(err)
	}
	want := `# HELP ucs_server_cpus CPUs (UCSM computeBlade.numOfCpus).
# TYPE ucs_server_cpus gauge
ucs_server_cpus{domain="ucs1",server="chassis-1/blade-1"} 2
# HELP ucs_server_info Server info.
# TYPE ucs_server_info gauge
ucs_server_info{domain="ucs1",model="B200",server="chassis-1/blade-1"} 1
ucs_server_info{domain="ucs1",model="B200",server="chassis-1/blade-2"} 1
`
	if buf.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", buf.String(), want)
	}
	if st := status["good"]; !st.OK() || st.Series != 3 || st.Dropped["empty_label"] != 1 {
		t.Errorf("good status = %+v", st)
	}
	if st := status["boom"]; st.OK() || st.Series != 0 || !strings.Contains(st.Err, "kaboom") {
		t.Errorf("boom status = %+v", st)
	}
	if st := status["failing"]; st.Err != "broken" {
		t.Errorf("failing status = %+v", st)
	}
}

func TestRenderDuplicates(t *testing.T) {
	d := NewDesc("ucs_x", "x", "a")
	dup := toy{name: "dup", collect: func(s *Snapshot, e *Emitter) error {
		e.Gauge(d, 1, "same")
		e.Gauge(d, 2, "same")
		e.Gauge(d, 3, "other")
		return nil
	}}
	fams, _, err := Render(snap(), []Module{dup}, nil)
	if err == nil {
		t.Error("duplicate series not reported")
	}
	if len(fams) != 1 || len(fams[0].Metric) != 2 {
		t.Errorf("families = %v", fams)
	}
}

func TestMergeQueries(t *testing.T) {
	f1 := ucsm.Ne("faultInst", "severity", "cleared")
	a := toy{name: "a", queries: []Query{
		{Class: "etherRxStats", Attrs: []string{"totalBytes"}},
		{Class: "faultInst", Filter: f1, Attrs: []string{"severity"}},
		{Class: "lsServer"},
	}}
	b := toy{name: "b", queries: []Query{
		{Class: "etherRxStats", Attrs: []string{"unicastPackets", "totalBytes"}},
		{Class: "faultInst", Filter: ucsm.Ne("faultInst", "severity", "cleared"), Attrs: []string{"type"}},
		{Class: "lsServer", Attrs: []string{"name"}},
		{Class: "fcStats", Filter: f1},
	}}
	c := toy{name: "c", queries: []Query{{Class: "fcStats"}}}
	got := MergeQueries([]Module{a, b, c})
	if cls := Classes(got); !slices.Equal(cls, []string{"etherRxStats", "faultInst", "fcStats", "lsServer"}) {
		t.Fatalf("classes = %v", cls)
	}
	if !slices.Equal(got[0].Attrs, []string{"suspect", "totalBytes", "unicastPackets"}) {
		t.Errorf("etherRxStats attrs = %v", got[0].Attrs)
	}
	// Only statistics classes get the suspect attribute.
	if got[1].Filter == nil || !slices.Equal(got[1].Attrs, []string{"severity", "type"}) {
		t.Errorf("faultInst = %+v", got[1])
	}
	if got[2].Filter != nil {
		t.Error("mixed filters not dropped")
	}
	if got[3].Attrs != nil {
		t.Errorf("lsServer attrs = %v, want nil (all)", got[3].Attrs)
	}
	keep := got[0].KeepFunc()
	if !keep("etherRxStats", "totalBytes") || keep("etherRxStats", "totalBytesDelta") {
		t.Error("KeepFunc mismatch")
	}
	if got[3].KeepFunc() != nil {
		t.Error("KeepFunc for all attrs should be nil")
	}
}

func TestEmitterHelpers(t *testing.T) {
	e := NewEmitter()
	st := NewDesc("ucs_x_state", "x", "id", "state")
	b := NewDesc("ucs_x_up", "x", "id")
	e.State(st, "", "1")
	e.Bool(b, true, "1")
	mo := ucsm.NewMO("x", "x", "v", "2", "na", "not-applicable")
	if !e.Attr(b, prometheus.GaugeValue, mo, "v", 0.5, "2") || e.Attr(b, prometheus.GaugeValue, mo, "na", 1, "3") {
		t.Error("Attr result mismatch")
	}
	e.Gauge(b, 1, "a", "extra") // wrong cardinality
	if len(e.Metrics()) != 3 || e.Dropped()["invalid"] != 1 || len(e.Errors()) != 1 {
		t.Errorf("metrics=%d dropped=%v errors=%v", len(e.Metrics()), e.Dropped(), e.Errors())
	}
}

func TestDescInfo(t *testing.T) {
	d := NewDesc("ucs_x_total", `Help with "quotes" (UCSM a.b).`, "server", "state")
	info, ok := DescInfo(d)
	if !ok || info.Name != "ucs_x_total" || info.Help != `Help with "quotes" (UCSM a.b).` || info.Type != "counter" ||
		strings.Join(info.Labels, ",") != "server,state" {
		t.Errorf("DescInfo = %+v, %v", info, ok)
	}
	if info, ok := DescInfo(NewDesc("ucs_y", "y")); !ok || info.Type != "gauge" || info.Labels != nil {
		t.Errorf("DescInfo(no labels) = %+v, %v", info, ok)
	}
}
