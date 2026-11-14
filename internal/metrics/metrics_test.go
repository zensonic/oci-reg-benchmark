// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

func TestRecorderCountsAndHistograms(t *testing.T) {
	r := NewRecorder(true, time.Now())

	for i := 1; i <= 100; i++ {
		r.Record(Result{
			Operation:    "pull",
			Start:        time.Now(),
			Latency:      time.Duration(i) * time.Millisecond,
			Total:        time.Duration(i) * 2 * time.Millisecond,
			Bytes:        1_000_000,
			TransferTime: time.Second,
			Phases:       map[string]time.Duration{"dns": time.Millisecond},
		})
	}
	r.Record(Result{Operation: "pull", Err: errors.New("boom"), ErrorType: "http_503"})

	reports, samples, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}
	rep := reports[0]
	if rep.Count != 101 || rep.Errors["http_503"] != 1 || rep.ErrorExamples["http_503"] != "boom" {
		t.Errorf("unexpected counts: %+v", rep)
	}
	if len(samples) != 101 {
		t.Errorf("got %d samples, want 101", len(samples))
	}

	h, err := Decode(rep.Histograms[protocol.MetricLatency])
	if err != nil {
		t.Fatal(err)
	}
	if h.TotalCount() != 100 {
		t.Errorf("latency histogram has %d values, want 100 (failures excluded)", h.TotalCount())
	}
	summary := LatencySummaryOf(h)
	if summary.P50Millis < 49.9 || summary.P50Millis > 50.1 {
		t.Errorf("p50 = %v ms, want about 50", summary.P50Millis)
	}

	through, err := Decode(rep.Histograms[protocol.MetricThroughput])
	if err != nil {
		t.Fatal(err)
	}
	if got := ThroughputPercentilesOf(through).P50; got < 990_000 || got > 1_010_000 {
		t.Errorf("throughput p50 = %v, want about 1e6", got)
	}
	if _, ok := rep.Histograms["phase:dns"]; !ok {
		t.Error("missing phase histogram")
	}

	// A second flush starts a fresh interval.
	reports, _, _ = r.Flush()
	if len(reports) != 0 {
		t.Errorf("second flush returned %d reports, want 0", len(reports))
	}
}

func TestMergingAcrossAgentsKeepsPercentilesCorrect(t *testing.T) {
	fast := NewRecorder(false, time.Now())
	slow := NewRecorder(false, time.Now())
	for i := 0; i < 90; i++ {
		fast.Record(Result{Operation: "push", Latency: 10 * time.Millisecond, Total: 10 * time.Millisecond})
	}
	for i := 0; i < 10; i++ {
		slow.Record(Result{Operation: "push", Latency: time.Second, Total: time.Second})
	}

	set := HistogramSet{}
	for _, r := range []*Recorder{fast, slow} {
		reports, _, _ := r.Flush()
		if err := set.MergeEncoded("push", protocol.MetricLatency, reports[0].Histograms[protocol.MetricLatency]); err != nil {
			t.Fatal(err)
		}
	}
	s := LatencySummaryOf(set["push"])
	if s.P50Millis > 10.1 || s.P95Millis < 999 {
		t.Errorf("merged percentiles wrong: p50=%v p95=%v", s.P50Millis, s.P95Millis)
	}
}

func TestValuesAreClamped(t *testing.T) {
	h := NewHistogram(protocol.MetricLatency)
	RecordDuration(h, 2*time.Hour)
	RecordDuration(h, 0)
	if h.TotalCount() != 2 {
		t.Errorf("clamped values were dropped: count=%d", h.TotalCount())
	}
}
