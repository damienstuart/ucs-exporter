// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func TestSplitDN(t *testing.T) {
	cases := []struct {
		dn   string
		want []string
	}{
		{"", nil},
		{"sys", []string{"sys"}},
		{"sys/chassis-1/blade-2", []string{"sys", "chassis-1", "blade-2"}},
		{"org-root/ls-[web/01]/ether-eth0", []string{"org-root", "ls-[web/01]", "ether-eth0"}},
		{"org-root/ls-[a[b/c]d]/x", []string{"org-root", "ls-[a[b/c]d]", "x"}},
		{"a//b", []string{"a", "", "b"}},
	}
	for _, c := range cases {
		if got := SplitDN(c.dn); !slices.Equal(got, c.want) {
			t.Errorf("SplitDN(%q) = %q, want %q", c.dn, got, c.want)
		}
	}
}

func TestParentAndLastRN(t *testing.T) {
	cases := []struct{ dn, parent, last string }{
		{"sys", "", "sys"},
		{"sys/chassis-1/blade-2", "sys/chassis-1", "blade-2"},
		{"org-root/ls-[web/01]", "org-root", "ls-[web/01]"},
		{"org-root/ls-[web/01]/ether-eth0", "org-root/ls-[web/01]", "ether-eth0"},
	}
	for _, c := range cases {
		if got := ParentDN(c.dn); got != c.parent {
			t.Errorf("ParentDN(%q) = %q, want %q", c.dn, got, c.parent)
		}
		if got := LastRN(c.dn); got != c.last {
			t.Errorf("LastRN(%q) = %q, want %q", c.dn, got, c.last)
		}
	}
}

func TestRNValue(t *testing.T) {
	if v, ok := RNValue("ls-[a/b]", "ls-"); !ok || v != "a/b" {
		t.Errorf("RNValue bracketed = %q, %v", v, ok)
	}
	if v, ok := RNValue("ls-esx01", "ls-"); !ok || v != "esx01" {
		t.Errorf("RNValue plain = %q, %v", v, ok)
	}
	if _, ok := RNValue("ether-eth0", "ls-"); ok {
		t.Error("RNValue matched wrong prefix")
	}
}

func FuzzSplitDN(f *testing.F) {
	for _, s := range []string{"sys/chassis-1", "org-root/ls-[a/b]/c", "[[/]]/x", "/", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, dn string) {
		rns := SplitDN(dn)
		if dn == "" {
			if rns != nil {
				t.Fatalf("SplitDN(\"\") = %q", rns)
			}
			return
		}
		if got := strings.Join(rns, "/"); got != dn {
			t.Fatalf("join(SplitDN(%q)) = %q", dn, got)
		}
		if p, l := ParentDN(dn), LastRN(dn); p != "" && p+"/"+l != dn {
			t.Fatalf("ParentDN+LastRN(%q) = %q + %q", dn, p, l)
		}
	})
}

func TestParseNumber(t *testing.T) {
	good := map[string]float64{"0": 0, "12": 12, " 3.5 ": 3.5, "-1": -1, "1e3": 1000}
	for in, want := range good {
		if got, ok := ParseNumber(in); !ok || got != want {
			t.Errorf("ParseNumber(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "not-applicable", "NA", "N/A", "unspecified", "unknown", "NaN", "Inf", "10gbps", "abc"} {
		if _, ok := ParseNumber(in); ok {
			t.Errorf("ParseNumber(%q) succeeded", in)
		}
	}
}

func TestMOAccessors(t *testing.T) {
	mo := &MO{Class: "computeBlade", DN: "sys/chassis-1/blade-1", Attrs: []Attr{
		{"numOfCpus", "2"}, {"totalMemory", "262144"}, {"model", "UCSB-B200-M5"}, {"temp", "not-applicable"},
	}}
	if v := mo.Get("model"); v != "UCSB-B200-M5" {
		t.Errorf("Get = %q", v)
	}
	if v := mo.Get("dn"); v != mo.DN {
		t.Errorf("Get(dn) = %q", v)
	}
	if v, ok := mo.Float("totalMemory"); !ok || v != 262144 {
		t.Errorf("Float = %v %v", v, ok)
	}
	if _, ok := mo.Float("temp"); ok {
		t.Error("Float(not-applicable) succeeded")
	}
	if _, ok := mo.Float("missing"); ok {
		t.Error("Float(missing) succeeded")
	}
	if v, ok := mo.Uint("numOfCpus"); !ok || v != 2 {
		t.Errorf("Uint = %v %v", v, ok)
	}
	n := 0
	for range mo.All() {
		n++
	}
	if n != 5 {
		t.Errorf("All yielded %d attributes, want 5", n)
	}
}

func TestSanitizer(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<a b="x < y"/>`, `<a b="x &lt; y"/>`},
		{`<a b="R&D"/>`, `<a b="R&amp;D"/>`},
		{`<a b="&amp; &lt; &#34; &#x41; &nbsp;"/>`, `<a b="&amp; &lt; &#34; &#x41; &nbsp;"/>`},
		{`<a b="&#; &#xZ; &; &1a;"/>`, `<a b="&amp;#; &amp;#xZ; &amp;; &amp;1a;"/>`},
		{`<a b='it"s <'/>`, `<a b='it"s &lt;'/>`},
		{"<a b=\"x\x01y\"/>", `<a b="x y"/>`},
		{"<a b=\"x\xffy\"/>", "<a b=\"x�y\"/>"},
		{"<a>x & y</a>", "<a>x &amp; y</a>"},
		{"<a b=\"tab\tnl\n\"/>", "<a b=\"tab\tnl\n\"/>"},
		{"<a b=\"café 中\"/>", "<a b=\"café 中\"/>"},
	}
	for _, c := range cases {
		for name, wrap := range map[string]func(io.Reader) io.Reader{
			"plain":   func(r io.Reader) io.Reader { return r },
			"onebyte": iotest.OneByteReader,
			"half":    iotest.HalfReader,
		} {
			got, err := io.ReadAll(newSanitizer(wrap(strings.NewReader(c.in))))
			if err != nil {
				t.Fatalf("%s %q: %v", name, c.in, err)
			}
			if string(got) != c.want {
				t.Errorf("%s: sanitize(%q) = %q, want %q", name, c.in, got, c.want)
			}
		}
	}
}

func TestDecodeObjects(t *testing.T) {
	in := `<configResolveClass cookie="c" response="yes" classId="fcErrStats">
  <outConfigs>
    <fcErrStats dn="sys/switch-A/slot-1/switch-fc/port-1/err-stats" crcRx="5" crcRxDelta="0" linkFailures="1" descr="bad <thing> & more"/>
    <fcErrStats dn="sys/switch-B/slot-1/switch-fc/port-1/err-stats" crcRx="0" crcRxDelta="0" linkFailures="0"/>
  </outConfigs>
</configResolveClass>`
	res, err := Decode(strings.NewReader(in), "configResolveClass", DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 2 {
		t.Fatalf("got %d objects", len(res.Objects))
	}
	mo := res.Objects[0]
	if mo.Class != "fcErrStats" || mo.DN != "sys/switch-A/slot-1/switch-fc/port-1/err-stats" {
		t.Errorf("object = %+v", mo)
	}
	if v := mo.Get("descr"); v != "bad <thing> & more" {
		t.Errorf("descr = %q", v)
	}
	if res.Attrs["classId"] != "fcErrStats" {
		t.Errorf("root attrs = %v", res.Attrs)
	}

	// Projection keeps only selected attributes (dn always).
	res, err = Decode(strings.NewReader(in), "configResolveClass", DecodeOptions{Keep: func(class, a string) bool {
		return class == "fcErrStats" && a == "crcRx"
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Objects[1].Attrs; len(got) != 1 || got[0].Name != "crcRx" || res.Objects[1].DN == "" {
		t.Errorf("projected object = %+v", res.Objects[1])
	}
}

func TestDecodeHierarchical(t *testing.T) {
	in := `<configResolveDn dn="sys/chassis-1" cookie="c" response="yes"><outConfig>
<equipmentChassis dn="sys/chassis-1" id="1"><equipmentPsu rn="psu-1" id="1"><equipmentPsuStats rn="stats" outputPower="400"/></equipmentPsu></equipmentChassis>
</outConfig></configResolveDn>`
	res, err := Decode(strings.NewReader(in), "configResolveDn", DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var dns []string
	for _, mo := range res.Objects {
		dns = append(dns, mo.Class+"="+mo.DN)
	}
	want := []string{"equipmentChassis=sys/chassis-1", "equipmentPsu=sys/chassis-1/psu-1", "equipmentPsuStats=sys/chassis-1/psu-1/stats"}
	if !slices.Equal(dns, want) {
		t.Errorf("objects = %q, want %q", dns, want)
	}
}

func TestDecodeErrors(t *testing.T) {
	cases := []struct {
		name, in, code string
	}{
		{"root errorCode", `<configResolveClass cookie="" response="yes" errorCode="552" invocationResult="unidentified-fail" errorDescr="Authorization required"/>`, "552"},
		{"error element", `<error cookie="" response="yes" errorCode="ERR-xml-parse-error" invocationResult="594" errorDescr="bad"/>`, "ERR-xml-parse-error"},
	}
	for _, c := range cases {
		_, err := Decode(strings.NewReader(c.in), "configResolveClass", DecodeOptions{})
		var ae *APIError
		if !errors.As(err, &ae) || ae.Code != c.code {
			t.Errorf("%s: err = %v, want APIError %s", c.name, err, c.code)
		}
	}
	if _, err := Decode(strings.NewReader(`<configResolveClass cookie="c" errorCode="552"/>`), "configResolveClass", DecodeOptions{}); !IsAuthRequired(err) {
		t.Errorf("IsAuthRequired(%v) = false", err)
	}

	for name, in := range map[string]string{
		"empty":     "",
		"truncated": `<configResolveClass cookie="c"><outConfigs><x dn="a"/>`,
		"wrong":     `<aaaLogin outCookie="x"/>`,
		"garbage":   `this is not xml`,
	} {
		if _, err := Decode(strings.NewReader(in), "configResolveClass", DecodeOptions{}); err == nil || IsAPIError(err) {
			t.Errorf("%s: err = %v, want decode error", name, err)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(`<configResolveClass><outConfigs><a dn="x" b="1"/></outConfigs></configResolveClass>`)
	f.Add(`<error errorCode="1"/>`)
	f.Add("<configResolveClass b=\"<&\xff\"><outConfigs><a rn=\"r\"><b rn=\"s\"/></a></outConfigs></configResolveClass>")
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = Decode(strings.NewReader(in), "configResolveClass", DecodeOptions{})
	})
}

func TestRequestMarshal(t *testing.T) {
	b, err := LoginRequest(`ucs-CORP\svc`, `p<&>"'`).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := `<aaaLogin inName="ucs-CORP\svc" inPassword="p&lt;&amp;&gt;&#34;&#39;"/>`
	if string(b) != want {
		t.Errorf("login = %s\nwant    %s", b, want)
	}

	b, _ = ResolveClassRequest("ck", "faultInst", Ne("faultInst", "severity", "cleared"), false).Marshal()
	if !strings.Contains(string(And(Ne("a", "b", "c"), Not(Eq("a", "d", "e"))).element().Name), "and") {
		t.Error("logic filter element")
	}
	want = `<configResolveClass cookie="ck" classId="faultInst" inHierarchical="false"><inFilter><ne class="faultInst" property="severity" value="cleared"/></inFilter></configResolveClass>`
	if string(b) != want {
		t.Errorf("resolveClass = %s\nwant           %s", b, want)
	}

	b, _ = ResolveClassesRequest("ck", []string{"computeBlade", "lsServer"}, false).Marshal()
	want = `<configResolveClasses cookie="ck" inHierarchical="false"><inIds><classId value="computeBlade"/><classId value="lsServer"/></inIds></configResolveClasses>`
	if string(b) != want {
		t.Errorf("resolveClasses = %s\nwant             %s", b, want)
	}

	b, _ = LogoutRequest("ck").Marshal()
	if string(b) != `<aaaLogout inCookie="ck"/>` {
		t.Errorf("logout = %s", b)
	}
}

func TestRedact(t *testing.T) {
	in := `<aaaLogin inName="u" inPassword="secret"/><x cookie="abc" outCookie="def" inCookie="ghi"/>`
	got := string(Redact([]byte(in)))
	for _, s := range []string{"secret", "abc", "def", "ghi"} {
		if strings.Contains(got, s) {
			t.Errorf("Redact left %q in %s", s, got)
		}
	}
	if !strings.Contains(got, `inName="u"`) {
		t.Errorf("Redact removed inName: %s", got)
	}
}

func TestFilterMatch(t *testing.T) {
	mo := &MO{Class: "faultInst", DN: "sys/chassis-1/fault-F0283", Attrs: []Attr{{"severity", "major"}, {"occur", "12"}}}
	wc, err := Wcard("faultInst", "dn", "^sys/chassis-")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		f    Filter
		want bool
	}{
		{Eq("faultInst", "severity", "major"), true},
		{Ne("faultInst", "severity", "cleared"), true},
		{Gt("faultInst", "occur", "9"), true}, // numeric, not lexical
		{Lt("faultInst", "occur", "9"), false},
		{Ge("faultInst", "occur", "12"), true},
		{Le("faultInst", "occur", "11"), false},
		{wc, true},
		{And(Eq("faultInst", "severity", "major"), Gt("faultInst", "occur", "100")), false},
		{Or(Eq("faultInst", "severity", "critical"), Eq("faultInst", "severity", "major")), true},
		{Not(Eq("faultInst", "severity", "major")), false},
	}
	for _, c := range cases {
		if got := c.f.Match(mo); got != c.want {
			t.Errorf("%s.Match = %v, want %v", c.f, got, c.want)
		}
	}
}

func TestParseFilterExpr(t *testing.T) {
	cases := map[string]string{
		"severity!=cleared":   `ne(faultInst.severity,"cleared")`,
		"severity = major":    `eq(faultInst.severity,"major")`,
		"occur>=3":            `ge(faultInst.occur,"3")`,
		"occur<3":             `lt(faultInst.occur,"3")`,
		"dn~^sys/chassis-1/":  `wcard(faultInst.dn,"^sys/chassis-1/")`,
		"descr~a=b":           `wcard(faultInst.descr,"a=b")`,
		"descr=x!=y":          `eq(faultInst.descr,"x!=y")`,
		"occur<=3":            `le(faultInst.occur,"3")`,
		"occur>3":             `gt(faultInst.occur,"3")`,
		"code=F0283":          `eq(faultInst.code,"F0283")`,
		"severity!=cleared  ": `ne(faultInst.severity,"cleared")`,
	}
	for in, want := range cases {
		f, err := ParseFilterExpr("faultInst", in)
		if err != nil {
			t.Errorf("ParseFilterExpr(%q): %v", in, err)
			continue
		}
		if f.String() != want {
			t.Errorf("ParseFilterExpr(%q) = %s, want %s", in, f, want)
		}
	}
	for _, bad := range []string{"severity", "=x", "dn~("} {
		if _, err := ParseFilterExpr("faultInst", bad); err == nil {
			t.Errorf("ParseFilterExpr(%q) succeeded", bad)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"ucs1":                      "https://ucs1/nuova",
		"ucs1:8443":                 "https://ucs1:8443/nuova",
		"https://ucs1":              "https://ucs1/nuova",
		"https://ucs1/":             "https://ucs1/nuova",
		"http://10.0.0.1:80/nuova":  "http://10.0.0.1:80/nuova",
		" https://ucs1/custom?x=1 ": "https://ucs1/custom",
	}
	for in, want := range cases {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) succeeded", bad)
		}
	}
}

func TestDecodeRejectsTruncation(t *testing.T) {
	// The tolerant parser closes every open element at a stray end tag;
	// the objects after it must not be silently dropped.
	in := `<configResolveClass cookie="c"><outConfigs><faultInst dn="a"></bogus><faultInst dn="b"/><faultInst dn="c"/></outConfigs></configResolveClass>`
	if res, err := Decode(strings.NewReader(in), "configResolveClass", DecodeOptions{}); err == nil {
		t.Errorf("decoded %d objects from a malformed response without error", len(res.Objects))
	}
	ok := "<configResolveClass cookie=\"c\"><outConfigs/></configResolveClass>\n<!-- trailer -->\n"
	if _, err := Decode(strings.NewReader(ok), "configResolveClass", DecodeOptions{}); err != nil {
		t.Errorf("trailing whitespace/comment rejected: %v", err)
	}
}

func TestSanitizerIllegalCharRefs(t *testing.T) {
	in := `<configResolveClass cookie="c"><outConfigs><x dn="a" descr="bell&#7;null&#0;esc&#x1b;ok&#x41;&#65;"/></outConfigs></configResolveClass>`
	res, err := Decode(strings.NewReader(in), "configResolveClass", DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Objects[0].Get("descr"); got != "bell&#7;null&#0;esc&#x1b;okAA" {
		t.Errorf("descr = %q", got)
	}
}
