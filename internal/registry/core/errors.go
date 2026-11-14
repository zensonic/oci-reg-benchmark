// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
)

// HTTPError is returned when the registry answers with an unexpected status.
type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	// Body is the start of the response body, which usually explains the error.
	Body string
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%s %s: unexpected status %d %s", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// newHTTPError reads the start of the body to explain the error and closes it.
func newHTTPError(resp *http.Response) *HTTPError {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &HTTPError{
		Method:     resp.Request.Method,
		Path:       resp.Request.URL.Path,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(body)),
	}
}

// IsStatus reports whether err is an HTTPError with the given status code.
func IsStatus(err error, status int) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == status
}

// ErrUnsupported is returned by operations the registry does not support.
var ErrUnsupported = errors.New("operation not supported by this registry")

// ClassifyError turns an error into a short error type for results (RES-3):
// "http_<status>", "timeout", "connection", "canceled" or "other".
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("http_%d", httpErr.StatusCode)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	if errors.Is(err, context.Canceled) {
		return "canceled"
	}

	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return "connection"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "connection"
	}

	return "other"
}
