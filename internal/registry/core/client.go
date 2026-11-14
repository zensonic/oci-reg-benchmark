// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package core is ORB's own small OCI Distribution client (REG-1, LAT-7).
//
// It is built directly on net/http and httptrace so that every timestamp
// ORB reports is exact: when an operation started, when the first byte of a
// response arrived, and how long DNS, connect, TLS and auth took. Every
// registry goes through this package. It contains no registry-specific code
// (REG-2); that lives in the registry modules.
package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zensonic/oci-reg-benchmark/internal/version"
)

// Config configures a Client.
type Config struct {
	// BaseURL of the registry, for example https://harbor.example.com.
	BaseURL string

	// Credentials for the registry. Nil means anonymous access.
	Credentials *Credentials

	// TLS settings. Nil means Go's defaults.
	TLS *tls.Config

	// Authenticator replaces the default challenge-based authenticator.
	// Registry modules use this for registries with their own token flow.
	Authenticator func(creds *Credentials, httpClient *http.Client) Authenticator
}

// Client talks to one registry. Each worker has its own Client, with its own
// connections and token cache, so it behaves like an independent client
// (LOAD-4). A Client is safe for concurrent use.
type Client struct {
	base       *url.URL
	httpClient *http.Client
	auth       Authenticator
	userAgent  string
}

// New creates a client.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimSuffix(cfg.BaseURL, "/"))
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("invalid registry URL %q", cfg.BaseURL)
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:     cfg.TLS,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 30 * time.Second,
	}
	httpClient := &http.Client{Transport: transport}

	c := &Client{
		base:       base,
		httpClient: httpClient,
		userAgent:  "orb/" + version.Version,
	}
	if cfg.Authenticator != nil {
		c.auth = cfg.Authenticator(cfg.Credentials, httpClient)
	} else {
		c.auth = newChallengeAuthenticator(cfg.Credentials, httpClient, c.userAgent)
	}
	return c, nil
}

// BaseURL returns the registry's base URL.
func (c *Client) BaseURL() *url.URL {
	copied := *c.base
	return &copied
}

// HTTPClient returns the underlying HTTP client. Registry modules use it to
// call registry-specific APIs over the same connections.
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

// Close releases the client's idle connections.
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}

// request describes one HTTP request to the registry.
type request struct {
	method string

	// path is relative to the base URL, for example "/v2/bench/app/tags/list".
	// Ignored if url is set.
	path  string
	query url.Values

	// url is an absolute URL, for example an upload Location from the registry.
	url *url.URL

	header http.Header

	// body is a replayable body. Requests with a body set this or stream.
	body []byte

	// stream is a body that can only be sent once. A request with a stream
	// cannot be retried after an auth challenge, so callers make sure a token
	// is cached first (by sending a bodyless request with the same scopes).
	stream       io.Reader
	streamLength int64

	// scopes are the bearer token scopes the request needs.
	scopes []string

	// expect lists the status codes that count as success.
	expect []int
}

// response is a successful response together with its trace.
type response struct {
	*http.Response
	trace *requestTrace
}

// do sends a request, handles auth challenges and checks the status code.
// The caller must close the response body.
func (c *Client) do(ctx context.Context, timing *Timing, r request) (*response, error) {
	resp, trace, err := c.send(ctx, timing, r)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized && r.stream == nil {
		spent, retry, authErr := c.auth.HandleChallenge(ctx, resp, r.scopes)
		timing.Add(PhaseAuth, spent)
		if authErr != nil {
			drainAndClose(resp)
			return nil, authErr
		}
		if retry {
			drainAndClose(resp)
			resp, trace, err = c.send(ctx, timing, r) //nolint:bodyclose // returned to the caller, which closes it
			if err != nil {
				return nil, err
			}
		}
	}

	if !containsStatus(r.expect, resp.StatusCode) {
		return nil, newHTTPError(resp)
	}
	return &response{Response: resp, trace: trace}, nil
}

// send builds and sends one HTTP request with tracing and credentials.
func (c *Client) send(ctx context.Context, timing *Timing, r request) (*http.Response, *requestTrace, error) {
	target := r.url
	if target == nil {
		target = c.base.ResolveReference(&url.URL{Path: r.path})
		if len(r.query) > 0 {
			target.RawQuery = r.query.Encode()
		}
	}

	var body io.Reader
	switch {
	case r.body != nil:
		body = bytes.NewReader(r.body)
	case r.stream != nil:
		body = r.stream
	}

	trace := &requestTrace{}
	req, err := http.NewRequestWithContext(withTrace(ctx, timing, trace), r.method, target.String(), body)
	if err != nil {
		return nil, nil, err
	}
	if r.body != nil {
		req.ContentLength = int64(len(r.body))
	} else if r.stream != nil {
		req.ContentLength = r.streamLength
	}
	for key, values := range r.header {
		req.Header[key] = values
	}
	req.Header.Set("User-Agent", c.userAgent)

	spent, err := c.auth.Authorize(ctx, req, r.scopes)
	timing.Add(PhaseAuth, spent)
	if err != nil {
		return nil, nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	return resp, trace, nil
}

// drainAndClose reads a little of the body so the connection can be reused.
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
}

func containsStatus(list []int, status int) bool {
	for _, s := range list {
		if s == status {
			return true
		}
	}
	return false
}

// repoPath builds a /v2/<repository>/<rest> path.
func repoPath(repository, rest string) string {
	return "/v2/" + repository + "/" + rest
}
