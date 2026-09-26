// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import (
	"bytes"
	"encoding/xml"
	"strconv"
)

// Element is an XML element of a request.
type Element struct {
	Name     string
	Attrs    []xml.Attr
	Children []Element
}

// write encodes e. Elements without children are written self-closing, as
// ucsmsdk does: UCS Manager's parser rejects "<ne ...></ne>" in filters.
// Attribute values are escaped, so passwords containing markup characters
// are sent correctly.
func (e Element) write(b *bytes.Buffer) {
	b.WriteByte('<')
	b.WriteString(e.Name)
	for _, a := range e.Attrs {
		b.WriteByte(' ')
		b.WriteString(a.Name.Local)
		b.WriteString(`="`)
		_ = xml.EscapeText(b, []byte(a.Value))
		b.WriteByte('"')
	}
	if len(e.Children) == 0 {
		b.WriteString("/>")
		return
	}
	b.WriteByte('>')
	for _, c := range e.Children {
		c.write(b)
	}
	b.WriteString("</")
	b.WriteString(e.Name)
	b.WriteByte('>')
}

// Request is a single UCSM XML API method call.
type Request struct {
	Method     string     // root element, e.g. "configResolveClass"
	Attrs      []xml.Attr // attributes, in order
	Children   []Element  // optional child elements (inFilter, inIds)
	Idempotent bool       // safe to retry on a stale connection
}

// Marshal encodes the request.
func (r Request) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	Element{Name: r.Method, Attrs: r.Attrs, Children: r.Children}.write(&buf)
	return buf.Bytes(), nil
}

func attr(name, value string) xml.Attr {
	return xml.Attr{Name: xml.Name{Local: name}, Value: value}
}

func boolAttr(name string, v bool) xml.Attr {
	return attr(name, strconv.FormatBool(v))
}

// LoginRequest builds aaaLogin.
func LoginRequest(user, password string) Request {
	return Request{Method: "aaaLogin", Attrs: []xml.Attr{
		attr("inName", user), attr("inPassword", password),
	}}
}

// RefreshRequest builds aaaRefresh, which exchanges a cookie for a new one.
func RefreshRequest(user, password, cookie string) Request {
	return Request{Method: "aaaRefresh", Attrs: []xml.Attr{
		attr("inName", user), attr("inPassword", password), attr("inCookie", cookie),
	}}
}

// LogoutRequest builds aaaLogout.
func LogoutRequest(cookie string) Request {
	return Request{Method: "aaaLogout", Attrs: []xml.Attr{attr("inCookie", cookie)}}
}

// ResolveClassRequest builds configResolveClass for one class, with an
// optional filter.
func ResolveClassRequest(cookie, class string, f Filter, hierarchical bool) Request {
	r := Request{Method: "configResolveClass", Idempotent: true, Attrs: []xml.Attr{
		attr("cookie", cookie), attr("classId", class), boolAttr("inHierarchical", hierarchical),
	}}
	if f != nil {
		r.Children = []Element{inFilter(f)}
	}
	return r
}

// ResolveClassesRequest builds configResolveClasses for several classes.
func ResolveClassesRequest(cookie string, classes []string, hierarchical bool) Request {
	ids := Element{Name: "inIds"}
	for _, c := range classes {
		ids.Children = append(ids.Children, Element{Name: "classId", Attrs: []xml.Attr{attr("value", c)}})
	}
	return Request{Method: "configResolveClasses", Idempotent: true,
		Attrs:    []xml.Attr{attr("cookie", cookie), boolAttr("inHierarchical", hierarchical)},
		Children: []Element{ids},
	}
}

// ResolveDnRequest builds configResolveDn.
func ResolveDnRequest(cookie, dn string, hierarchical bool) Request {
	return Request{Method: "configResolveDn", Idempotent: true, Attrs: []xml.Attr{
		attr("cookie", cookie), attr("dn", dn), boolAttr("inHierarchical", hierarchical),
	}}
}

// ResolveChildrenRequest builds configResolveChildren. class may be empty to
// return children of every class.
func ResolveChildrenRequest(cookie, dn, class string, f Filter, hierarchical bool) Request {
	r := Request{Method: "configResolveChildren", Idempotent: true, Attrs: []xml.Attr{attr("cookie", cookie)}}
	if class != "" {
		// UCS Manager rejects an empty classId ("no class named").
		r.Attrs = append(r.Attrs, attr("classId", class))
	}
	r.Attrs = append(r.Attrs, attr("inDn", dn), boolAttr("inHierarchical", hierarchical))
	if f != nil {
		r.Children = []Element{inFilter(f)}
	}
	return r
}
