// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// Phase names recorded for operations (LAT-6).
const (
	PhaseDNS           = "dns"
	PhaseConnect       = "connect"
	PhaseTLS           = "tls"
	PhaseAuth          = "auth"
	PhaseManifest      = "manifest"
	PhaseUploadSession = "uploadSession"
	PhaseFirstChunk    = "firstChunk"
	PhaseCommit        = "commit"
	PhaseTTFB          = "ttfb"
)

// Timing collects the timings of one operation. Every request made for the
// operation adds its DNS, connect and TLS time, and the client adds auth
// token requests. Operations add their own phases on top. It is safe for
// concurrent use.
type Timing struct {
	// Start is when the operation started. All latencies are measured from
	// here with Go's monotonic clock (LAT-1).
	Start time.Time

	mu     sync.Mutex
	phases map[string]time.Duration
}

// StartTiming begins timing an operation now.
func StartTiming() *Timing {
	return &Timing{Start: time.Now(), phases: map[string]time.Duration{}}
}

// Add adds a duration to a phase.
func (t *Timing) Add(phase string, d time.Duration) {
	if t == nil || d <= 0 {
		return
	}
	t.mu.Lock()
	t.phases[phase] += d
	t.mu.Unlock()
}

// Phases returns a copy of the recorded phases.
func (t *Timing) Phases() map[string]time.Duration {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]time.Duration, len(t.phases))
	for k, v := range t.phases {
		out[k] = v
	}
	return out
}

// Elapsed is the time since the operation started.
func (t *Timing) Elapsed() time.Duration {
	return time.Since(t.Start)
}

// ElapsedAt is the time from the operation start until the given moment.
func (t *Timing) ElapsedAt(moment time.Time) time.Duration {
	return moment.Sub(t.Start)
}

// requestTrace holds the trace timestamps of a single HTTP request.
type requestTrace struct {
	mu        sync.Mutex
	firstByte time.Time
}

// FirstByte is when the first byte of the response arrived.
func (r *requestTrace) FirstByte() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.firstByte
}

// withTrace attaches an httptrace that adds DNS, connect and TLS time to the
// operation's timing and remembers when the first response byte arrived.
func withTrace(ctx context.Context, timing *Timing, rt *requestTrace) context.Context {
	var dnsStart, connectStart, tlsStart time.Time
	var mu sync.Mutex

	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			mu.Lock()
			dnsStart = time.Now()
			mu.Unlock()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			mu.Lock()
			timing.Add(PhaseDNS, time.Since(dnsStart))
			mu.Unlock()
		},
		ConnectStart: func(string, string) {
			mu.Lock()
			connectStart = time.Now()
			mu.Unlock()
		},
		ConnectDone: func(string, string, error) {
			mu.Lock()
			timing.Add(PhaseConnect, time.Since(connectStart))
			mu.Unlock()
		},
		TLSHandshakeStart: func() {
			mu.Lock()
			tlsStart = time.Now()
			mu.Unlock()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			mu.Lock()
			timing.Add(PhaseTLS, time.Since(tlsStart))
			mu.Unlock()
		},
		GotFirstResponseByte: func() {
			rt.mu.Lock()
			rt.firstByte = time.Now()
			rt.mu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, trace)
}
