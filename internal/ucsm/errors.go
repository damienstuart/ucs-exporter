// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

package ucsm

import (
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// UCSM error codes this package classifies. UCSM reports API errors with
// HTTP 200 and errorCode/errorDescr attributes on the response element.
const (
	codeAuthRequired    = "552" // cookie missing, expired or invalid
	codeSessionNotFound = "555" // returned by aaaLogout for an unknown session
	codeMaxSessions     = "572" // user reached the maximum number of sessions
)

// APIError is an error reported by the UCSM XML API.
type APIError struct {
	Method           string // request method, e.g. "configResolveClass"
	Code             string // errorCode; numeric ("552") or symbolic ("ERR-xml-parse-error")
	InvocationResult string
	Descr            string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("ucsm %s: error %s", e.Method, e.Code)
	if e.InvocationResult != "" && e.InvocationResult != e.Code {
		s += " (" + e.InvocationResult + ")"
	}
	if e.Descr != "" {
		s += ": " + e.Descr
	}
	return s
}

func apiCode(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// IsAuthRequired reports whether err means the session cookie is no longer
// valid and a new login is needed.
func IsAuthRequired(err error) bool { return apiCode(err) == codeAuthRequired }

// IsSessionNotFound reports whether err means UCSM does not know the session.
func IsSessionNotFound(err error) bool { return apiCode(err) == codeSessionNotFound }

// IsMaxSessions reports whether err means the account has too many sessions.
func IsMaxSessions(err error) bool { return apiCode(err) == codeMaxSessions }

// IsAPIError reports whether err is (or wraps) an *APIError.
func IsAPIError(err error) bool {
	var ae *APIError
	return errors.As(err, &ae)
}

// HTTPStatusError is returned when UCSM answers with a non-200 status.
type HTTPStatusError struct {
	Status   int
	Location string // redirect target, if any
	Snippet  string // start of the response body
}

func (e *HTTPStatusError) Error() string {
	if e.Location != "" {
		return fmt.Sprintf("ucsm: HTTP %d redirect to %q; set the domain address to the redirect target", e.Status, e.Location)
	}
	if e.Snippet != "" {
		return fmt.Sprintf("ucsm: HTTP %d: %s", e.Status, e.Snippet)
	}
	return fmt.Sprintf("ucsm: HTTP %d", e.Status)
}

// TransportError wraps network, TLS and timeout errors.
type TransportError struct {
	Op  string
	Err error
}

func (e *TransportError) Error() string {
	msg := fmt.Sprintf("ucsm %s: %v", e.Op, e.Err)
	var uae x509.UnknownAuthorityError
	var hne x509.HostnameError
	var cie x509.CertificateInvalidError
	if errors.As(e.Err, &uae) || errors.As(e.Err, &hne) || errors.As(e.Err, &cie) {
		msg += " (set tls.ca_file to the UCSM CA certificate, or tls.insecure_skip_verify: true for this domain)"
	}
	return msg
}

func (e *TransportError) Unwrap() error { return e.Err }

// LoginBackoffError is returned while logins are suppressed after an
// authentication failure, to avoid locking out directory accounts.
type LoginBackoffError struct {
	Until time.Time
	Last  error
}

func (e *LoginBackoffError) Error() string {
	return fmt.Sprintf("ucsm: login suppressed until %s after previous failure: %v", e.Until.Format(time.RFC3339), e.Last)
}

func (e *LoginBackoffError) Unwrap() error { return e.Last }
