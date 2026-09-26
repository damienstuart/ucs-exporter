// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Package ucsmtest provides a fake UCS Manager XML API for tests and local
// development. It implements aaaLogin/aaaRefresh/aaaLogout with expiring
// cookies and the configResolve* methods over a set of fixture objects, and
// supports fault injection.
package ucsmtest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damienstuart/ucs-exporter/internal/ucsm"
)

// Rec records one request handled by the server.
type Rec struct {
	Method string
	Class  string // classId, if any
	DN     string // dn/inDn, if any
	Cookie string
	Start  time.Time
	End    time.Time
}

type apiErr struct{ code, descr string }

type session struct {
	user   string
	issued time.Time
}

// Server is a fake UCSM. The zero value is not usable; call New.
type Server struct {
	mu            sync.Mutex
	users         map[string]string
	refreshPeriod int
	maxSessions   int
	version       string
	priv          string
	sessions      map[string]*session
	nextCookie    int

	raw     map[string][]byte     // classId -> verbatim configResolveClass response
	byClass map[string][]*ucsm.MO // classId -> objects
	byDN    map[string]*ucsm.MO

	failClass   map[string]apiErr
	delayClass  map[string]time.Duration
	delayMethod map[string]time.Duration
	failHTTP    map[string]int // method -> remaining HTTP 500 responses
	failLogins  int

	requests    []Rec
	inflight    int
	maxInflight int
}

// Option configures a Server.
type Option func(*Server) error

// WithUser adds an account.
func WithUser(user, password string) Option {
	return func(s *Server) error { s.users[user] = password; return nil }
}

// WithRefreshPeriod sets outRefreshPeriod in seconds (default 600). Cookies
// not refreshed within the period expire.
func WithRefreshPeriod(seconds int) Option {
	return func(s *Server) error { s.refreshPeriod = seconds; return nil }
}

// WithMaxSessions limits concurrent sessions (default unlimited).
func WithMaxSessions(n int) Option {
	return func(s *Server) error { s.maxSessions = n; return nil }
}

// WithVersion sets outVersion.
func WithVersion(v string) Option {
	return func(s *Server) error { s.version = v; return nil }
}

// WithPriv sets outPriv.
func WithPriv(p string) Option {
	return func(s *Server) error { s.priv = p; return nil }
}

// WithFixtures loads every <classId>.xml in dir. Each file is a
// configResolveClass response as written by "ucs-exporter explore capture".
func WithFixtures(dir string) Option {
	return func(s *Server) error {
		files, err := filepath.Glob(filepath.Join(dir, "*.xml"))
		if err != nil {
			return err
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			class := strings.TrimSuffix(filepath.Base(f), ".xml")
			if err := s.addRaw(class, b); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		}
		return nil
	}
}

// WithObjects adds objects directly.
func WithObjects(mos ...*ucsm.MO) Option {
	return func(s *Server) error { s.AddObjects(mos...); return nil }
}

// New returns a fake UCSM. Without WithUser, the account admin/password is
// accepted.
func New(opts ...Option) (*Server, error) {
	s := &Server{
		users:         map[string]string{},
		refreshPeriod: 600,
		version:       "4.3(4a)",
		priv:          "read-only",
		sessions:      map[string]*session{},
		raw:           map[string][]byte{},
		byClass:       map[string][]*ucsm.MO{},
		byDN:          map[string]*ucsm.MO{},
		failClass:     map[string]apiErr{},
		delayClass:    map[string]time.Duration{},
		delayMethod:   map[string]time.Duration{},
		failHTTP:      map[string]int{},
	}
	for _, o := range opts {
		if err := o(s); err != nil {
			return nil, err
		}
	}
	if len(s.users) == 0 {
		s.users["admin"] = "password"
	}
	return s, nil
}

func (s *Server) addRaw(class string, b []byte) error {
	res, err := ucsm.Decode(bytes.NewReader(b), "configResolveClass", ucsm.DecodeOptions{})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw[class] = b
	for _, mo := range res.Objects {
		s.addLocked(mo)
	}
	return nil
}

// AddObjects adds objects to the server's inventory.
func (s *Server) AddObjects(mos ...*ucsm.MO) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, mo := range mos {
		delete(s.raw, mo.Class) // serve the combined set
		s.addLocked(mo)
	}
}

func (s *Server) addLocked(mo *ucsm.MO) {
	s.byClass[mo.Class] = append(s.byClass[mo.Class], mo)
	s.byDN[mo.DN] = mo
}

// UnknownClass makes queries for class fail the way UCS Manager rejects a
// class it does not know.
func (s *Server) UnknownClass(class string) {
	s.FailClass(class, "ERR-xml-parse-error", "XML PARSING ERROR: no class named "+class)
}

// FailClass makes queries for class fail with the given API error.
func (s *Server) FailClass(class, code, descr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failClass[class] = apiErr{code, descr}
}

// ClearFailures removes all FailClass entries.
func (s *Server) ClearFailures() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.failClass)
}

// DelayClass delays responses for class.
func (s *Server) DelayClass(class string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delayClass[class] = d
}

// DelayMethod delays responses to a method, e.g. "aaaLogout".
func (s *Server) DelayMethod(method string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delayMethod[method] = d
}

// FailHTTP makes the next n requests for method fail with HTTP 500.
func (s *Server) FailHTTP(method string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failHTTP[method] = n
}

// ExpireSessions invalidates every cookie.
func (s *Server) ExpireSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.sessions)
}

// FailLogins makes the next n logins fail with "Authentication failed".
func (s *Server) FailLogins(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failLogins = n
}

// Sessions returns the number of live sessions.
func (s *Server) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Requests returns the requests handled so far.
func (s *Server) Requests() []Rec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Rec(nil), s.requests...)
}

// Count returns the number of requests for method.
func (s *Server) Count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.Method == method {
			n++
		}
	}
	return n
}

// MaxInFlight returns the maximum number of concurrently handled requests.
func (s *Server) MaxInFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInflight
}

// RoundTripper returns an in-memory transport that calls the server
// directly, without sockets.
func (s *Server) RoundTripper() http.RoundTripper { return rt{s} }

type rt struct{ s *Server }

func (t rt) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	rec := httptest.NewRecorder()
	t.s.ServeHTTP(rec, r)
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

// NewTLSServer starts an HTTPS server for s. Close it when done; its
// Certificate() is the CA to trust.
func (s *Server) NewTLSServer() *httptest.Server { return httptest.NewTLSServer(s) }

// node is a generic XML element.
type node struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []node     `xml:",any"`
}

func (n node) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n node) child(name string) (node, bool) {
	for _, c := range n.Children {
		if c.XMLName.Local == name {
			return c, true
		}
	}
	return node{}, false
}

// ServeHTTP implements the /nuova endpoint.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req node
	if err := xml.Unmarshal(body, &req); err != nil {
		writeError(w, "error", "ERR-xml-parse-error", "XML PARSING ERROR: "+err.Error())
		return
	}
	method := req.XMLName.Local
	rec := Rec{Method: method, Class: req.attr("classId"), DN: req.attr("dn") + req.attr("inDn"), Cookie: req.attr("cookie"), Start: time.Now()}

	s.mu.Lock()
	s.inflight++
	s.maxInflight = max(s.maxInflight, s.inflight)
	delay := s.delayClass[rec.Class] + s.delayMethod[method]
	fail500 := s.failHTTP[method] > 0
	if fail500 {
		s.failHTTP[method]--
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inflight--
		rec.End = time.Now()
		s.requests = append(s.requests, rec)
		s.mu.Unlock()
	}()

	if delay > 0 {
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			return
		}
	}

	if fail500 {
		http.Error(w, "injected failure", http.StatusInternalServerError)
		return
	}
	switch method {
	case "aaaLogin":
		s.login(w, req)
	case "aaaRefresh":
		s.refresh(w, req)
	case "aaaLogout":
		s.logout(w, req)
	case "aaaKeepAlive":
		if !s.checkCookie(w, method, req.attr("cookie")) {
			return
		}
		writeResponse(w, method, []xml.Attr{attr("cookie", req.attr("cookie")), attr("response", "yes")}, "", nil)
	case "configResolveClass", "configResolveClasses", "configResolveDn", "configResolveChildren":
		if !s.checkCookie(w, method, req.attr("cookie")) {
			return
		}
		s.resolve(w, method, req)
	default:
		writeError(w, method, "ERR-xml-parse-error", "unknown method "+method)
	}
}

func (s *Server) login(w http.ResponseWriter, req node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, pass := req.attr("inName"), req.attr("inPassword")
	if s.failLogins > 0 {
		s.failLogins--
		writeError(w, "aaaLogin", "551", "Authentication failed")
		return
	}
	if want, ok := s.users[user]; !ok || want != pass {
		writeError(w, "aaaLogin", "551", "Authentication failed")
		return
	}
	if s.maxSessions > 0 && len(s.sessions) >= s.maxSessions {
		writeError(w, "aaaLogin", "572", "User reached maximum session limit")
		return
	}
	cookie := s.newCookieLocked(user)
	writeResponse(w, "aaaLogin", []xml.Attr{
		attr("cookie", ""), attr("response", "yes"),
		attr("outCookie", cookie), attr("outRefreshPeriod", strconv.Itoa(s.refreshPeriod)),
		attr("outPriv", s.priv), attr("outDomains", ""), attr("outChannel", "noencssl"),
		attr("outEvtChannel", "noencssl"), attr("outSessionId", "web_"+strconv.Itoa(s.nextCookie)),
		attr("outVersion", s.version), attr("outName", user),
	}, "", nil)
}

func (s *Server) newCookieLocked(user string) string {
	s.nextCookie++
	cookie := fmt.Sprintf("%d/fake-%08d", time.Now().Unix(), s.nextCookie)
	s.sessions[cookie] = &session{user: user, issued: time.Now()}
	return cookie
}

func (s *Server) validLocked(cookie string) (*session, bool) {
	sess, ok := s.sessions[cookie]
	if !ok {
		return nil, false
	}
	if time.Since(sess.issued) > time.Duration(s.refreshPeriod)*time.Second {
		delete(s.sessions, cookie)
		return nil, false
	}
	return sess, true
}

func (s *Server) refresh(w http.ResponseWriter, req node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := req.attr("inCookie")
	sess, ok := s.validLocked(old)
	if !ok {
		writeError(w, "aaaRefresh", "552", "Authorization required")
		return
	}
	if want := s.users[req.attr("inName")]; req.attr("inName") != sess.user || want != req.attr("inPassword") {
		writeError(w, "aaaRefresh", "551", "Authentication failed")
		return
	}
	delete(s.sessions, old)
	cookie := s.newCookieLocked(sess.user)
	writeResponse(w, "aaaRefresh", []xml.Attr{
		attr("cookie", ""), attr("response", "yes"),
		attr("outCookie", cookie), attr("outRefreshPeriod", strconv.Itoa(s.refreshPeriod)),
		attr("outPriv", s.priv), attr("outVersion", s.version),
	}, "", nil)
}

func (s *Server) logout(w http.ResponseWriter, req node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cookie := req.attr("inCookie")
	if _, ok := s.sessions[cookie]; !ok {
		writeError(w, "aaaLogout", "555", "Session not found")
		return
	}
	delete(s.sessions, cookie)
	writeResponse(w, "aaaLogout", []xml.Attr{attr("cookie", ""), attr("response", "yes"), attr("outStatus", "success")}, "", nil)
}

func (s *Server) checkCookie(w http.ResponseWriter, method, cookie string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.validLocked(cookie); !ok {
		writeError(w, method, "552", "Authorization required")
		return false
	}
	return true
}

func (s *Server) resolve(w http.ResponseWriter, method string, req node) {
	cookie := req.attr("cookie")
	hier := req.attr("inHierarchical") == "true"
	var filter ucsm.Filter
	if fn, ok := req.child("inFilter"); ok && len(fn.Children) > 0 {
		f, err := parseFilter(fn.Children[0])
		if err != nil {
			writeError(w, method, "ERR-xml-parse-error", err.Error())
			return
		}
		filter = f
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	base := []xml.Attr{attr("cookie", cookie), attr("response", "yes")}
	switch method {
	case "configResolveClass":
		class := req.attr("classId")
		if e, ok := s.failClass[class]; ok {
			writeError(w, method, e.code, e.descr)
			return
		}
		if raw, ok := s.raw[class]; ok && filter == nil && !hier {
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write(raw)
			return
		}
		writeResponse(w, method, append(base, attr("classId", class)), "outConfigs", s.selectLocked(s.byClass[class], filter, hier))
	case "configResolveClasses":
		var mos []*ucsm.MO
		if ids, ok := req.child("inIds"); ok {
			for _, id := range ids.Children {
				class := id.attr("value")
				if e, ok := s.failClass[class]; ok {
					writeError(w, method, e.code, e.descr)
					return
				}
				mos = append(mos, s.selectLocked(s.byClass[class], nil, hier)...)
			}
		}
		writeResponse(w, method, base, "outConfigs", mos)
	case "configResolveDn":
		dn := req.attr("dn")
		var mos []*ucsm.MO
		if mo, ok := s.byDN[dn]; ok {
			mos = s.selectLocked([]*ucsm.MO{mo}, nil, hier)
		}
		writeResponse(w, method, append(base, attr("dn", dn)), "outConfig", mos)
	case "configResolveChildren":
		dn, class := req.attr("inDn"), req.attr("classId")
		var kids []*ucsm.MO
		for _, mo := range s.byDN {
			if ucsm.ParentDN(mo.DN) == dn && (class == "" || mo.Class == class) {
				kids = append(kids, mo)
			}
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].DN < kids[j].DN })
		writeResponse(w, method, append(base, attr("classId", class)), "outConfigs", s.selectLocked(kids, filter, hier))
	}
}

// selectLocked applies f and, if hier, adds all descendants of each object.
func (s *Server) selectLocked(mos []*ucsm.MO, f ucsm.Filter, hier bool) []*ucsm.MO {
	var out []*ucsm.MO
	for _, mo := range mos {
		if f != nil && !f.Match(mo) {
			continue
		}
		out = append(out, mo)
		if hier {
			prefix := mo.DN + "/"
			var desc []*ucsm.MO
			for dn, d := range s.byDN {
				if strings.HasPrefix(dn, prefix) {
					desc = append(desc, d)
				}
			}
			sort.Slice(desc, func(i, j int) bool { return desc[i].DN < desc[j].DN })
			out = append(out, desc...)
		}
	}
	return out
}

func parseFilter(n node) (ucsm.Filter, error) {
	class, prop, value := n.attr("class"), n.attr("property"), n.attr("value")
	switch n.XMLName.Local {
	case "eq":
		return ucsm.Eq(class, prop, value), nil
	case "ne":
		return ucsm.Ne(class, prop, value), nil
	case "gt":
		return ucsm.Gt(class, prop, value), nil
	case "ge":
		return ucsm.Ge(class, prop, value), nil
	case "lt":
		return ucsm.Lt(class, prop, value), nil
	case "le":
		return ucsm.Le(class, prop, value), nil
	case "wcard":
		return ucsm.Wcard(class, prop, value)
	case "and", "or", "not":
		subs := make([]ucsm.Filter, 0, len(n.Children))
		for _, c := range n.Children {
			f, err := parseFilter(c)
			if err != nil {
				return nil, err
			}
			subs = append(subs, f)
		}
		switch n.XMLName.Local {
		case "and":
			return ucsm.And(subs...), nil
		case "or":
			return ucsm.Or(subs...), nil
		}
		if len(subs) != 1 {
			return nil, fmt.Errorf("not filter needs exactly one operand")
		}
		return ucsm.Not(subs[0]), nil
	}
	return nil, fmt.Errorf("unsupported filter %q", n.XMLName.Local)
}

func attr(name, value string) xml.Attr { return xml.Attr{Name: xml.Name{Local: name}, Value: value} }

func writeError(w http.ResponseWriter, method, code, descr string) {
	writeResponse(w, method, []xml.Attr{
		attr("cookie", ""), attr("response", "yes"),
		attr("errorCode", code), attr("invocationResult", "unidentified-fail"), attr("errorDescr", descr),
	}, "", nil)
}

// writeResponse writes <method attrs><container>objects</container></method>.
func writeResponse(w http.ResponseWriter, method string, attrs []xml.Attr, container string, mos []*ucsm.MO) {
	var buf bytes.Buffer
	_ = EncodeResponse(&buf, method, attrs, container, mos)
	w.Header().Set("Content-Type", "text/xml")
	_, _ = w.Write(buf.Bytes())
}

// EncodeResponse writes a UCSM-style response document.
func EncodeResponse(out io.Writer, method string, attrs []xml.Attr, container string, mos []*ucsm.MO) error {
	e := xml.NewEncoder(out)
	root := xml.StartElement{Name: xml.Name{Local: method}, Attr: attrs}
	if err := e.EncodeToken(root); err != nil {
		return err
	}
	if container != "" {
		c := xml.StartElement{Name: xml.Name{Local: container}}
		if err := e.EncodeToken(c); err != nil {
			return err
		}
		for _, mo := range mos {
			a := make([]xml.Attr, 0, len(mo.Attrs)+1)
			a = append(a, attr("dn", mo.DN))
			for _, x := range mo.Attrs {
				a = append(a, attr(x.Name, x.Value))
			}
			el := xml.StartElement{Name: xml.Name{Local: mo.Class}, Attr: a}
			if err := e.EncodeToken(el); err != nil {
				return err
			}
			if err := e.EncodeToken(el.End()); err != nil {
				return err
			}
		}
		if err := e.EncodeToken(c.End()); err != nil {
			return err
		}
	}
	if err := e.EncodeToken(root.End()); err != nil {
		return err
	}
	return e.Flush()
}
