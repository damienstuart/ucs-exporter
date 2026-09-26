// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import (
	"bufio"
	"io"
	"strconv"
	"unicode/utf8"
)

// UCSM occasionally returns XML that encoding/xml rejects: raw '<' or bare
// '&' inside attribute values (e.g. in fault descriptions), control
// characters, and invalid UTF-8. ucsmsdk works around the same problem by
// re-escaping and re-parsing (ucsgenutils.remove_invalid_chars). The
// sanitizer below repairs these in a single streaming pass:
//
//   - '<' inside a quoted attribute value becomes "&lt;"
//   - '&' that does not start a well-formed reference becomes "&amp;"
//   - C0 control characters other than tab, CR and LF become a space
//   - invalid UTF-8 and the non-characters U+FFFE/U+FFFF become U+FFFD
//
// It tracks just enough XML syntax (text / inside tag / inside quotes) to
// know where it is; comments and CDATA are not expected in UCSM responses.

const (
	stText = iota
	stTag
	stQuote
)

// maxRefLen bounds the lookahead used to validate an entity reference.
const maxRefLen = 40

type sanitizer struct {
	r     *bufio.Reader
	buf   []byte
	out   []byte
	state int
	quote rune
	err   error
}

func newSanitizer(r io.Reader) io.Reader {
	return &sanitizer{r: bufio.NewReaderSize(r, 64<<10), buf: make([]byte, 0, 32<<10)}
}

func (s *sanitizer) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		s.fill()
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

func (s *sanitizer) fill() {
	out := s.buf[:0]
	for len(out) < cap(s.buf)-8 {
		r, size, err := s.r.ReadRune()
		if err != nil {
			s.err = err
			break
		}
		switch {
		case r == utf8.RuneError && size == 1, r == 0xFFFE, r == 0xFFFF:
			out = utf8.AppendRune(out, utf8.RuneError)
			continue
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			out = append(out, ' ')
			continue
		}
		switch s.state {
		case stText:
			switch r {
			case '<':
				s.state = stTag
			case '&':
				out = s.amp(out)
				continue
			}
		case stTag:
			switch r {
			case '"', '\'':
				s.state, s.quote = stQuote, r
			case '>':
				s.state = stText
			}
		case stQuote:
			switch r {
			case s.quote:
				s.state = stTag
			case '<':
				out = append(out, "&lt;"...)
				continue
			case '&':
				out = s.amp(out)
				continue
			}
		}
		out = utf8.AppendRune(out, r)
	}
	s.out = out
}

// amp appends either '&' or "&amp;" depending on whether the bytes following
// the (already consumed) '&' form a valid entity or character reference.
func (s *sanitizer) amp(out []byte) []byte {
	b, _ := s.r.Peek(maxRefLen)
	if validRef(b) {
		return append(out, '&')
	}
	return append(out, "&amp;"...)
}

// validRef reports whether b starts with "name;", "#digits;" or "#xhex;".
func validRef(b []byte) bool {
	end := -1
	for i, c := range b {
		if c == ';' {
			end = i
			break
		}
	}
	if end <= 0 {
		return false
	}
	ref := b[:end]
	if ref[0] == '#' {
		digits, isHex := ref[1:], false
		if len(digits) > 0 && (digits[0] == 'x' || digits[0] == 'X') {
			digits, isHex = digits[1:], true
		}
		if len(digits) == 0 || len(digits) > 8 {
			return false
		}
		for _, c := range digits {
			if !isDigit(c) && !(isHex && isHexLetter(c)) {
				return false
			}
		}
		base := 10
		if isHex {
			base = 16
		}
		n, err := strconv.ParseUint(string(digits), base, 32)
		return err == nil && xmlChar(rune(n))
	}
	for i, c := range ref {
		if isLetter(c) || c == '_' || c == ':' || (i > 0 && (isDigit(c) || c == '-' || c == '.')) {
			continue
		}
		return false
	}
	return true
}

// xmlChar reports whether r is allowed in an XML 1.0 document.
func xmlChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}

func isDigit(c byte) bool     { return c >= '0' && c <= '9' }
func isHexLetter(c byte) bool { return (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }
func isLetter(c byte) bool    { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
