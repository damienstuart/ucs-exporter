// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package poller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/damienstuart/ucs-exporter/internal/config"
	"github.com/damienstuart/ucs-exporter/internal/module"
	"github.com/damienstuart/ucs-exporter/internal/ucsm"
	"github.com/damienstuart/ucs-exporter/internal/ucsm/ucsmtest"
)

// bladeModule emits one gauge per computeBlade and counts equipmentChassis.
type bladeModule struct{}

var (
	bladeCPUs     = module.NewDesc("ucs_test_blade_cpus", "CPUs.", "server")
	chassisCount  = module.NewDesc("ucs_test_chassis", "Chassis.")
	bladeCPUTable = module.NewTable("computeBlade", "ucs_test_blade", []string{"server"}, module.G("numOfCpus", "cpus", "CPUs"))
)

func (bladeModule) Name() string        { return "blades" }
func (bladeModule) Description() string { return "test" }
func (bladeModule) Queries() []module.Query {
	return []module.Query{{Class: "computeBlade", Attrs: []string{"numOfCpus"}}, {Class: "equipmentChassis"}}
}
func (bladeModule) Describe(ch chan<- *prometheus.Desc) { ch <- bladeCPUs; ch <- chassisCount }
func (bladeModule) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, mo := range s.Class("computeBlade") {
		bladeCPUTable.Emit(e, mo, strings.TrimPrefix(mo.DN, "sys/"))
	}
	e.Gauge(chassisCount, float64(len(s.Class("equipmentChassis"))))
	return nil
}

// rxModule exports one counter per etherRxStats object.
type rxModule struct{}

var rxTable = module.NewTable("etherRxStats", "ucs_test_rx", []string{"dn"}, module.C("totalBytes", "bytes_total", "Bytes received"))

func (rxModule) Name() string        { return "rx" }
func (rxModule) Description() string { return "test" }
func (rxModule) Queries() []module.Query {
	return []module.Query{{Class: "etherRxStats", Attrs: rxTable.Attrs()}}
}
func (rxModule) Describe(ch chan<- *prometheus.Desc) { rxTable.Describe(ch) }
func (rxModule) Collect(s *module.Snapshot, e *module.Emitter) error {
	for _, mo := range s.Class("etherRxStats") {
		rxTable.Emit(e, mo, mo.DN)
	}
	return nil
}

func fake(t *testing.T, opts ...ucsmtest.Option) *ucsmtest.Server {
	t.Helper()
	opts = append([]ucsmtest.Option{
		ucsmtest.WithUser("mon", "pw"),
		ucsmtest.WithObjects(
			ucsm.NewMO("computeBlade", "sys/chassis-1/blade-1", "numOfCpus", "2", "model", "B200"),
			ucsm.NewMO("computeBlade", "sys/chassis-1/blade-2", "numOfCpus", "4", "model", "B200"),
			ucsm.NewMO("equipmentChassis", "sys/chassis-1", "id", "1"),
		),
	}, opts...)
	s, err := ucsmtest.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func resolved(t *testing.T, extra string) config.Resolved {
	t.Helper()
	cfg, err := config.Parse([]byte("domains: [{name: ucs1, username: mon, password: pw"+extra+"}]"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := cfg.Lookup("ucs1")
	return r
}

func newPoller(t *testing.T, s *ucsmtest.Server, r config.Resolved) *Poller {
	t.Helper()
	p, err := New(Options{Config: r, Modules: []module.Module{bladeModule{}}, Transport: s.RoundTripper()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func text(t *testing.T, st *State) string {
	t.Helper()
	var buf bytes.Buffer
	if err := module.WriteText(&buf, st.Families); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestPollSuccess(t *testing.T) {
	s := fake(t)
	p := newPoller(t, s, resolved(t, ""))
	st := p.PollOnce(context.Background())
	if st.Result != ResultSuccess || !st.Up() || st.Err != nil {
		t.Fatalf("state = %+v", st)
	}
	out := text(t, st)
	for _, want := range []string{
		`ucs_test_blade_cpus{domain="ucs1",server="chassis-1/blade-1"} 2`,
		`ucs_test_blade_cpus{domain="ucs1",server="chassis-1/blade-2"} 4`,
		`ucs_test_chassis{domain="ucs1"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if cs := st.Classes["computeBlade"]; cs.Objects != 2 || cs.Err != nil || cs.Stale {
		t.Errorf("class status = %+v", cs)
	}
	expected := `
# HELP ucs_up Whether the latest poll of the UCS domain succeeded at least partially (logged in and retrieved at least one class).
# TYPE ucs_up gauge
ucs_up{domain="ucs1"} 1
# HELP ucs_class_query_success Whether the latest query for a UCSM class succeeded.
# TYPE ucs_class_query_success gauge
ucs_class_query_success{class="computeBlade",domain="ucs1"} 1
ucs_class_query_success{class="equipmentChassis",domain="ucs1"} 1
# HELP ucs_polls_total Polls by result (success, partial, failure).
# TYPE ucs_polls_total counter
ucs_polls_total{domain="ucs1",result="failure"} 0
ucs_polls_total{domain="ucs1",result="partial"} 0
ucs_polls_total{domain="ucs1",result="success"} 1
`
	if err := testutil.CollectAndCompare(p.Health(st), strings.NewReader(expected), "ucs_up", "ucs_class_query_success", "ucs_polls_total"); err != nil {
		t.Error(err)
	}
	if problems, err := testutil.CollectAndLint(p.Health(st)); err != nil || len(problems) > 0 {
		t.Errorf("lint: %v %v", problems, err)
	}
}

func TestPollPartialCarryForward(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := fake(t)
		p := newPoller(t, s, resolved(t, ", max_data_age: 3m"))
		ctx := context.Background()
		p.PollOnce(ctx)

		s.UnknownClass("computeBlade")
		time.Sleep(time.Minute)
		st := p.PollOnce(ctx)
		if st.Result != ResultPartial || !st.Up() {
			t.Fatalf("result = %s", st.Result)
		}
		cs := st.Classes["computeBlade"]
		if cs.Err == nil || !cs.Stale || cs.Objects != 2 {
			t.Errorf("class status = %+v", cs)
		}
		if out := text(t, st); !strings.Contains(out, `server="chassis-1/blade-1"`) {
			t.Errorf("stale data not carried forward:\n%s", out)
		}

		// After max_data_age the stale data is dropped.
		time.Sleep(3 * time.Minute)
		st = p.PollOnce(ctx)
		if out := text(t, st); strings.Contains(out, "ucs_test_blade_cpus") || st.Classes["computeBlade"].Stale {
			t.Errorf("expired data still present:\n%s", out)
		}
		if st.Classes["computeBlade"].LastSuccess.IsZero() {
			t.Error("last success forgotten")
		}
	})
}

func TestPollSuspectStats(t *testing.T) {
	const good, bad = "sys/switch-A/slot-1/switch-ether/port-1/rx-stats", "sys/switch-A/slot-1/switch-ether/port-2/rx-stats"
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprintf("skip=%t", skip), func(t *testing.T) {
			s := fake(t, ucsmtest.WithObjects(
				ucsm.NewMO("etherRxStats", good, "totalBytes", "100", "suspect", "no"),
				ucsm.NewMO("etherRxStats", bad, "totalBytes", "465042103975542800", "suspect", "yes"),
			))
			p, err := New(Options{Config: resolved(t, fmt.Sprintf(", skip_suspect_stats: %t", skip)),
				Modules: []module.Module{bladeModule{}, rxModule{}}, Transport: s.RoundTripper()})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			st := p.PollOnce(ctx)
			out := text(t, st)
			if !strings.Contains(out, `dn="`+good+`"`) {
				t.Errorf("object that is not suspect missing:\n%s", out)
			}
			if exported := strings.Contains(out, `dn="`+bad+`"`); exported == skip {
				t.Errorf("suspect object exported = %v with skip_suspect_stats: %v\n%s", exported, skip, out)
			}
			objects := 2
			if skip {
				objects = 1
			}
			if cs := st.Classes["etherRxStats"]; cs.Suspect != 1 || cs.Objects != objects {
				t.Errorf("class status = %+v", cs)
			}
			// Reported whether or not suspect objects are skipped, and only
			// for statistics classes.
			expected := `
# HELP ucs_class_suspect_objects Objects of a UCSM statistics class that UCSM flagged as suspect (unreliable) in the latest snapshot. They are left out when skip_suspect_stats is enabled.
# TYPE ucs_class_suspect_objects gauge
ucs_class_suspect_objects{class="etherRxStats",domain="ucs1"} 1
`
			if err := testutil.CollectAndCompare(p.Health(st), strings.NewReader(expected), "ucs_class_suspect_objects"); err != nil {
				t.Error(err)
			}

			// The count is carried forward with stale data.
			s.UnknownClass("etherRxStats")
			st = p.PollOnce(ctx)
			if cs := st.Classes["etherRxStats"]; !cs.Stale || cs.Suspect != 1 || cs.Objects != objects {
				t.Errorf("stale class status = %+v", cs)
			}
		})
	}
}

func TestPollLoginFailure(t *testing.T) {
	s := fake(t)
	r := resolved(t, "")
	r.Password = "wrong"
	p := newPoller(t, s, r)
	st := p.PollOnce(context.Background())
	if st.Result != ResultFailure || st.Up() || !ucsm.IsAPIError(st.Err) {
		t.Fatalf("state = %+v", st)
	}
	if strings.Contains(text(t, st), "ucs_test_blade_cpus") {
		t.Error("device metrics present after failed login")
	}
	for class, cs := range st.Classes {
		if cs.Err == nil {
			t.Errorf("class %s has no error", class)
		}
	}
}

func TestRunNoOverlapAndLogout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := fake(t)
		s.DelayClass("computeBlade", 50*time.Second) // longer than the 45s timeout
		p := newPoller(t, s, resolved(t, ", interval: 30s, timeout: 45s, request_timeout: 45s"))
		ctx, cancel := context.WithCancel(context.Background())
		go p.Run(ctx)
		time.Sleep(5 * time.Minute)
		st := p.State()
		cancel()
		<-p.Done()

		var last time.Time
		n := 0
		for _, r := range s.Requests() {
			if r.Class != "computeBlade" {
				continue
			}
			n++
			if r.Start.Before(last) {
				t.Errorf("overlapping queries: start %v before previous end %v", r.Start, last)
			}
			last = r.End
		}
		if n < 3 {
			t.Errorf("only %d polls", n)
		}
		if cs := st.Classes["computeBlade"]; !errors.Is(cs.Err, context.DeadlineExceeded) {
			t.Errorf("slow class err = %v", cs.Err)
		}
		if p.overruns == 0 {
			t.Error("no overruns recorded")
		}
		if s.Sessions() != 0 || s.Count("aaaLogout") != 1 {
			t.Errorf("sessions = %d, logouts = %d after stop", s.Sessions(), s.Count("aaaLogout"))
		}
	})
}

func TestRunRefreshBetweenPolls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := fake(t, ucsmtest.WithRefreshPeriod(120))
		p := newPoller(t, s, resolved(t, ", interval: 5m"))
		ctx, cancel := context.WithCancel(context.Background())
		go p.Run(ctx)
		time.Sleep(11 * time.Minute)
		cancel()
		<-p.Done()
		if s.Count("aaaLogin") != 1 {
			t.Errorf("logins = %d, want 1 (session kept alive by refresh)", s.Count("aaaLogin"))
		}
		if s.Count("aaaRefresh") < 4 {
			t.Errorf("refreshes = %d", s.Count("aaaRefresh"))
		}
		if st := p.State(); st.Result != ResultSuccess {
			t.Errorf("result = %s", st.Result)
		}
	})
}

func TestWaitFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := fake(t)
		p, err := New(Options{Config: resolved(t, ""), Modules: []module.Module{bladeModule{}}, Transport: s.RoundTripper(), Jitter: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer func() { cancel(); <-p.Done() }()
		if p.State() != nil {
			t.Fatal("state before first poll")
		}
		go p.Run(ctx)
		wctx, wcancel := context.WithTimeout(ctx, 20*time.Second)
		defer wcancel()
		if err := p.WaitFirst(wctx); err != nil {
			t.Fatalf("WaitFirst: %v", err)
		}
		if p.State() == nil {
			t.Fatal("no state after WaitFirst")
		}
	})
}

func TestLastSuccessSurvivesDataExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := fake(t)
		p := newPoller(t, s, resolved(t, ", max_data_age: 0s"))
		ctx := context.Background()
		first := p.PollOnce(ctx).Classes["computeBlade"].LastSuccess
		s.UnknownClass("computeBlade")
		for range 2 {
			time.Sleep(time.Minute)
			if got := p.PollOnce(ctx).Classes["computeBlade"].LastSuccess; !got.Equal(first) {
				t.Errorf("LastSuccess = %v, want %v", got, first)
			}
		}
	})
}
