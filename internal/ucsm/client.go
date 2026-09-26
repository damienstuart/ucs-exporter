// SPDX-FileCopyrightText: 2026 The ucs-exporter authors
//
// SPDX-License-Identifier: GPL-3.0-only

// Package ucsm is a minimal client for the Cisco UCS Manager XML API: login,
// session refresh, logout and the configResolve* query methods. It has no
// Prometheus dependencies.
package ucsm

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultMaxResponseBytes bounds a single response body.
const DefaultMaxResponseBytes = 256 << 20

// ErrResponseTooLarge is returned when a response exceeds MaxResponseBytes.
var ErrResponseTooLarge = errors.New("ucsm: response exceeds size limit")

// ClientConfig configures a Client.
type ClientConfig struct {
	// Address is "host", "host:port" or an http(s) URL. "/nuova" is appended
	// when no path is given; https is assumed when no scheme is given.
	Address string
	// TLS configures certificate verification. nil verifies against the
	// system roots.
	TLS *tls.Config
	// ProxyURL, if set, routes requests through an HTTP proxy. The proxy
	// environment variables are deliberately ignored.
	ProxyURL *url.URL
	// MaxConns limits concurrent connections to UCSM (default 3).
	MaxConns int
	// MaxResponseBytes bounds a response body (default 256 MiB).
	MaxResponseBytes int64
	// UserAgent is sent with every request.
	UserAgent string
	// Transport overrides the HTTP transport (tests).
	Transport http.RoundTripper
	// Trace, if set, receives every request and response body with
	// passwords and cookies redacted.
	Trace func(direction, method string, body []byte)
}

// Client sends XML API requests to one UCSM.
type Client struct {
	url      string
	hc       *http.Client
	maxBytes int64
	ua       string
	trace    func(direction, method string, body []byte)
}

// NormalizeURL turns a configured address into the XML API endpoint URL.
func NormalizeURL(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("empty address")
	}
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	u, err := url.Parse(address)
	if err != nil {
		return "", fmt.Errorf("invalid address %q: %w", address, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("invalid address %q: scheme must be https or http", address)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid address %q: missing host", address)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/nuova"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// NewClient returns a client for cfg.Address.
func NewClient(cfg ClientConfig) (*Client, error) {
	endpoint, err := NormalizeURL(cfg.Address)
	if err != nil {
		return nil, err
	}
	rt := cfg.Transport
	if rt == nil {
		n := cfg.MaxConns
		if n <= 0 {
			n = 3
		}
		tlsCfg := cfg.TLS
		if tlsCfg == nil {
			tlsCfg = &tls.Config{}
		}
		tr := &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:     tlsCfg,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        n,
			MaxIdleConnsPerHost: n,
			MaxConnsPerHost:     n,
			IdleConnTimeout:     90 * time.Second,
		}
		if cfg.ProxyURL != nil {
			tr.Proxy = http.ProxyURL(cfg.ProxyURL)
		}
		// UCSM's web server speaks HTTP/1.1; avoid negotiating anything else.
		tr.Protocols = new(http.Protocols)
		tr.Protocols.SetHTTP1(true)
		rt = tr
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	return &Client{
		url: endpoint,
		hc: &http.Client{
			Transport: rt,
			// A redirected POST would be replayed as a GET without a body;
			// report the redirect instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxBytes: maxBytes,
		ua:       cfg.UserAgent,
		trace:    cfg.Trace,
	}, nil
}

// URL returns the XML API endpoint.
func (c *Client) URL() string { return c.url }

// CloseIdleConnections closes idle keep-alive connections.
func (c *Client) CloseIdleConnections() { c.hc.CloseIdleConnections() }

// Do sends req and decodes the response.
func (c *Client) Do(ctx context.Context, req Request, opt DecodeOptions) (*Result, error) {
	body, err := c.post(ctx, req)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	res, err := Decode(body, req.Method, opt)
	if err != nil && !IsAPIError(err) && !errors.Is(err, ErrResponseTooLarge) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &TransportError{Op: req.Method, Err: ctxErr}
		}
	}
	return res, err
}

// DoRaw sends req and returns the raw response body. If the body is an API
// error response, the body is returned together with the *APIError.
func (c *Client) DoRaw(ctx context.Context, req Request) ([]byte, error) {
	body, err := c.post(ctx, req)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, &TransportError{Op: req.Method, Err: err}
	}
	if _, err := Decode(bytes.NewReader(raw), req.Method, DecodeOptions{Keep: keepNone}); IsAPIError(err) {
		return raw, err
	}
	return raw, nil
}

func keepNone(string, string) bool { return false }

func (c *Client) post(ctx context.Context, req Request) (io.ReadCloser, error) {
	payload, err := req.Marshal()
	if err != nil {
		return nil, fmt.Errorf("ucsm %s: encoding request: %w", req.Method, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ucsm %s: %w", req.Method, err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/xml, text/xml")
	if c.ua != "" {
		httpReq.Header.Set("User-Agent", c.ua)
	}
	if req.Idempotent {
		// A nil-valued idempotency key is not sent, but tells net/http that
		// the POST may be retried on a stale keep-alive connection.
		httpReq.Header["X-Idempotency-Key"] = nil
	}
	if c.trace != nil {
		c.trace("request", req.Method, Redact(payload))
	}
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, &TransportError{Op: req.Method, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &HTTPStatusError{
			Status:   resp.StatusCode,
			Location: resp.Header.Get("Location"),
			Snippet:  strings.TrimSpace(string(Redact(snippet))),
		}
	}
	body := io.ReadCloser(&limitedBody{rc: resp.Body, remaining: c.maxBytes})
	if c.trace != nil {
		raw, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			return nil, &TransportError{Op: req.Method, Err: err}
		}
		c.trace("response", req.Method, Redact(raw))
		body = io.NopCloser(bytes.NewReader(raw))
	}
	return body, nil
}

type limitedBody struct {
	rc        io.ReadCloser
	remaining int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		// Distinguish "exactly at the limit" from "over the limit".
		var one [1]byte
		if n, _ := l.rc.Read(one[:]); n > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.rc.Read(p)
	l.remaining -= int64(n)
	return n, err
}

func (l *limitedBody) Close() error { return l.rc.Close() }

var secretAttr = regexp.MustCompile(`\b(inPassword|inCookie|outCookie|cookie)="[^"]*"`)

// Redact replaces passwords and session cookies in an XML payload.
func Redact(b []byte) []byte {
	return secretAttr.ReplaceAll(b, []byte(`$1="<redacted>"`))
}
