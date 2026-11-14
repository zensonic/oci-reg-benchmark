// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// testReport builds a report with one pull operation.
func testReport(t *testing.T, scenario string, interval int) protocol.IntervalReport {
	t.Helper()
	rec := metrics.NewRecorder(true, time.Now())
	rec.Record(metrics.Result{Operation: "pull", Start: time.Now(), Latency: 5 * time.Millisecond, Total: 9 * time.Millisecond, Bytes: 100})
	rec.Record(metrics.Result{Operation: "pull", Err: errors.New("boom"), ErrorType: "http_503", Counters: map[string]int64{"pages": 2}})
	ops, samples, err := rec.Flush()
	if err != nil {
		t.Fatal(err)
	}
	return protocol.IntervalReport{
		ScenarioName: scenario, Interval: interval, Stage: 0, Workers: 3,
		CPUUtilization: 0.5, Operations: ops, Samples: samples,
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.EnsureRun("uid-1", "default", "run-1"); err != nil {
		t.Fatal(err)
	}
	for agent := 0; agent < 2; agent++ {
		for interval := 0; interval < 3; interval++ {
			if err := s.InsertReport("uid-1", agent, testReport(t, "load", interval)); err != nil {
				t.Fatal(err)
			}
		}
	}

	scenarios, err := s.Scenarios("uid-1")
	if err != nil || len(scenarios) != 1 || scenarios[0] != "load" {
		t.Fatalf("scenarios %v, %v", scenarios, err)
	}
	intervals, _ := s.Intervals("uid-1", "load")
	if len(intervals) != 6 {
		t.Errorf("got %d interval rows, want 6", len(intervals))
	}
	ops, _ := s.Operations("uid-1", "load")
	if len(ops) != 6 || ops[0].Count != 2 || ops[0].Errors != 1 || ops[0].Bytes != 100 {
		t.Errorf("unexpected operations %+v", ops)
	}
	histograms := 0
	if err := s.EachHistogram("uid-1", "load", protocol.MetricLatency, func(HistogramRow) error { histograms++; return nil }); err != nil {
		t.Fatal(err)
	}
	if histograms != 6 {
		t.Errorf("got %d latency histograms, want 6", histograms)
	}
	counters, _ := s.Counters("uid-1", "load")
	if len(counters) != 12 { // one error type and one counter per row
		t.Errorf("got %d counters, want 12", len(counters))
	}
	examples, _ := s.ErrorExamples("uid-1", "load")
	if len(examples) != 1 || examples[0].Message != "boom" {
		t.Errorf("unexpected examples %+v", examples)
	}
	samples, _ := s.Samples("uid-1", "load")
	if len(samples) != 12 {
		t.Errorf("got %d samples, want 12", len(samples))
	}
}

func TestExportAndDelete(t *testing.T) {
	s := openTestStore(t)
	for _, uid := range []string{"keep", "other"} {
		if err := s.EnsureRun(uid, "default", uid); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertReport(uid, 0, testReport(t, "load", 0)); err != nil {
			t.Fatal(err)
		}
	}

	exportPath := filepath.Join(t.TempDir(), "export.db")
	if err := s.ExportRun("keep", exportPath); err != nil {
		t.Fatal(err)
	}
	exported, err := Open(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Close()
	if ok, _ := exported.HasRun("keep"); !ok {
		t.Error("export is missing the run")
	}
	if ok, _ := exported.HasRun("other"); ok {
		t.Error("export contains another run")
	}

	if err := s.DeleteRun("keep"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.HasRun("keep"); ok {
		t.Error("run was not deleted")
	}
	if ok, _ := s.HasRun("other"); !ok {
		t.Error("deleting one run removed another")
	}
}

func TestReopenKeepsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orb.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	s.Close()
}
