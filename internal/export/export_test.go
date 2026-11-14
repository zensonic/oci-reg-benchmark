// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package export

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

func report(t *testing.T, interval int, warmup bool, latency time.Duration) protocol.IntervalReport {
	t.Helper()
	rec := metrics.NewRecorder(false, time.Now())
	for i := 0; i < 10; i++ {
		rec.Record(metrics.Result{
			Operation: "pull", Latency: latency, Total: 2 * latency, Bytes: 10,
			Phases: map[string]time.Duration{"dns": time.Millisecond},
		})
	}
	rec.Record(metrics.Result{Operation: "pull", Err: errors.New("unavailable"), ErrorType: "http_503"})
	ops, _, err := rec.Flush()
	if err != nil {
		t.Fatal(err)
	}
	return protocol.IntervalReport{ScenarioName: "load", Interval: interval, Warmup: warmup, Workers: 2, Operations: ops}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.EnsureRun("uid", "default", "run"); err != nil {
		t.Fatal(err)
	}
	for agent := 0; agent < 2; agent++ {
		// Interval 0 is warm-up with very slow operations; they must not
		// show up in the phase summaries.
		if err := st.InsertReport("uid", agent, report(t, 0, true, time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertReport("uid", agent, report(t, 1, false, 10*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestBuildDetail(t *testing.T) {
	detail, err := BuildDetail(testStore(t), "uid")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Scenarios) != 1 {
		t.Fatalf("got %d scenarios", len(detail.Scenarios))
	}
	sd := detail.Scenarios[0]
	if len(sd.Intervals) != 2 {
		t.Fatalf("got %d intervals, want 2", len(sd.Intervals))
	}
	second := sd.Intervals[1]
	if second.Workers != 4 || len(second.Operations) != 1 {
		t.Fatalf("interval not merged across agents: %+v", second)
	}
	op := second.Operations[0]
	if op.Count != 22 || op.Errors != 2 || op.Bytes != 200 || op.P50Millis < 9.9 || op.P50Millis > 10.1 {
		t.Errorf("unexpected interval figures %+v", op)
	}

	for _, p := range sd.Phases {
		if p.Metric == protocol.MetricLatency && p.Latency.MaxMillis > 11 {
			t.Errorf("warm-up leaked into phase summary: %+v", p)
		}
	}
	if len(sd.Agents) != 4 {
		t.Errorf("got %d agent intervals, want 4", len(sd.Agents))
	}
	if len(sd.Errors) != 1 || sd.Errors[0].Count != 4 || sd.Errors[0].Example != "unavailable" {
		t.Errorf("unexpected errors %+v", sd.Errors)
	}
}

func TestHandler(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, testStore(t))
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + protocol.ExportPath("uid"))
	if err != nil {
		t.Fatal(err)
	}
	var detail protocol.Detail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil || detail.RunUID != "uid" {
		t.Fatalf("decoding JSON export: %v, %+v", err, detail)
	}
	resp.Body.Close()

	resp, err = http.Get(server.URL + protocol.ExportPath("uid") + "?format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/vnd.sqlite3" {
		t.Errorf("sqlite export: status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	resp, err = http.Get(server.URL + protocol.ExportPath("missing"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing run: status %d, want 404", resp.StatusCode)
	}
}
