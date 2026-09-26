// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// Result is a decoded UCSM response.
type Result struct {
	Method  string            // response root element, normally the request method
	Attrs   map[string]string // root attributes, e.g. outCookie for aaaLogin
	Objects []*MO             // managed objects from outConfig(s), flattened
}

// DecodeOptions controls response decoding.
type DecodeOptions struct {
	// Keep reports whether an attribute of an object of the given class is
	// retained. nil keeps every attribute. The dn is always retained.
	Keep func(class, attr string) bool
}

// Decode parses a UCSM response to the given request method. It returns an
// *APIError when UCSM reports an error. Objects nested inside other objects
// (hierarchical responses) are flattened into Result.Objects; a missing dn is
// derived from the parent's dn and the object's rn.
func Decode(r io.Reader, method string, opt DecodeOptions) (*Result, error) {
	d := xml.NewDecoder(newSanitizer(r))
	d.Strict = false
	// The sanitizer has already normalised the input to UTF-8.
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }

	root, err := firstStart(d)
	if err != nil {
		return nil, fmt.Errorf("ucsm %s: decoding response: %w", method, err)
	}
	res := &Result{Method: root.Name.Local, Attrs: make(map[string]string, len(root.Attr))}
	for _, a := range root.Attr {
		res.Attrs[a.Name.Local] = a.Value
	}
	if code := res.Attrs["errorCode"]; root.Name.Local == "error" || (code != "" && code != "0") {
		return nil, &APIError{
			Method:           method,
			Code:             code,
			InvocationResult: res.Attrs["invocationResult"],
			Descr:            res.Attrs["errorDescr"],
		}
	}
	if root.Name.Local != method {
		return nil, fmt.Errorf("ucsm %s: unexpected response element <%s>", method, root.Name.Local)
	}

	type frame struct {
		mo     *MO  // object at this level, if any
		config bool // inside outConfig(s)
	}
	stack := []frame{{}} // root
	names := map[string]string{}
	intern := func(s string) string {
		if v, ok := names[s]; ok {
			return v
		}
		names[s] = s
		return s
	}

	for {
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("ucsm %s: truncated response", method)
			}
			return nil, fmt.Errorf("ucsm %s: decoding response: %w", method, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			top := stack[len(stack)-1]
			switch {
			case top.config:
				mo := newMO(t, top.mo, opt.Keep, intern)
				res.Objects = append(res.Objects, mo)
				stack = append(stack, frame{mo: mo, config: true})
			case len(stack) == 1 && (t.Name.Local == "outConfigs" || t.Name.Local == "outConfig"):
				stack = append(stack, frame{config: true})
			default:
				stack = append(stack, frame{})
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				if err := expectEnd(d); err != nil {
					return nil, fmt.Errorf("ucsm %s: %w", method, err)
				}
				return res, nil
			}
		}
	}
}

// expectEnd checks that nothing but whitespace, comments and processing
// instructions follows the root element. The non-strict decoder closes all
// open elements at a mismatched end tag, which would otherwise silently
// truncate the response.
func expectEnd(d *xml.Decoder) error {
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(t)) != 0 {
				return errors.New("malformed response: data after the root element")
			}
		case xml.Comment, xml.ProcInst, xml.Directive:
		default:
			return errors.New("malformed response: elements after the root element (mismatched tags?)")
		}
	}
}

func firstStart(d *xml.Decoder) (xml.StartElement, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return xml.StartElement{}, errors.New("empty response")
			}
			return xml.StartElement{}, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se, nil
		}
	}
}

func newMO(t xml.StartElement, parent *MO, keep func(class, attr string) bool, intern func(string) string) *MO {
	mo := &MO{Class: intern(t.Name.Local)}
	var rn string
	n := 0
	for _, a := range t.Attr {
		switch a.Name.Local {
		case "dn":
			mo.DN = a.Value
			continue
		case "rn":
			rn = a.Value
		}
		if keep == nil || keep(mo.Class, a.Name.Local) {
			n++
		}
	}
	if n > 0 {
		mo.Attrs = make([]Attr, 0, n)
		for _, a := range t.Attr {
			if a.Name.Local == "dn" || (keep != nil && !keep(mo.Class, a.Name.Local)) {
				continue
			}
			mo.Attrs = append(mo.Attrs, Attr{Name: intern(a.Name.Local), Value: a.Value})
		}
	}
	if mo.DN == "" && rn != "" {
		if parent != nil && parent.DN != "" {
			mo.DN = parent.DN + "/" + rn
		} else {
			mo.DN = rn
		}
	}
	return mo
}
