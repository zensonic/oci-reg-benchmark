// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package metrics records operation results in HDR histograms, turns them
// into interval reports, merges histograms from many agents, and computes
// percentile summaries (RES-2).
//
// Latencies are stored in microseconds. Throughput is stored in bytes per
// second. Histograms are always merged, never averaged, so percentiles stay
// correct across agents.
package metrics

import (
	"fmt"
	"strings"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

const (
	// maxLatencyMicros is the longest latency a histogram can hold: one hour.
	maxLatencyMicros = int64(time.Hour / time.Microsecond)

	// maxThroughput is the highest throughput a histogram can hold: 1 TB/s.
	maxThroughput = int64(1_000_000_000_000)

	// latencyFigures gives 0.1% precision for latencies.
	latencyFigures = 3

	// throughputFigures gives 1% precision for throughput.
	throughputFigures = 2
)

// NewHistogram returns an empty histogram suited to the given metric name.
func NewHistogram(metric string) *hdr.Histogram {
	if metric == protocol.MetricThroughput {
		return hdr.New(1, maxThroughput, throughputFigures)
	}
	return hdr.New(1, maxLatencyMicros, latencyFigures)
}

// RecordDuration adds a duration to a latency histogram. Values outside the
// histogram's range are clamped, so nothing is silently dropped.
func RecordDuration(h *hdr.Histogram, d time.Duration) {
	recordClamped(h, d.Microseconds())
}

// RecordValue adds a plain value, such as bytes per second, to a histogram.
func RecordValue(h *hdr.Histogram, v int64) {
	recordClamped(h, v)
}

func recordClamped(h *hdr.Histogram, v int64) {
	v = max(v, h.LowestTrackableValue())
	v = min(v, h.HighestTrackableValue())
	_ = h.RecordValue(v) // cannot fail after clamping
}

// Encode turns a histogram into the compressed V2 encoding used on the wire
// and in SQLite.
func Encode(h *hdr.Histogram) ([]byte, error) {
	return h.Encode(hdr.V2CompressedEncodingCookieBase)
}

// Decode reads a histogram written by Encode.
func Decode(data []byte) (*hdr.Histogram, error) {
	h, err := hdr.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("decoding histogram: %w", err)
	}
	return h, nil
}

// HistogramSet is a set of histograms keyed by name, used to merge
// histograms from many agents and intervals.
type HistogramSet map[string]*hdr.Histogram

// MergeEncoded decodes an encoded histogram and merges it into the set.
func (s HistogramSet) MergeEncoded(key, metric string, data []byte) error {
	h, err := Decode(data)
	if err != nil {
		return err
	}
	s.Merge(key, metric, h)
	return nil
}

// Merge adds the values of h into the histogram with the given key.
func (s HistogramSet) Merge(key, metric string, h *hdr.Histogram) {
	target, ok := s[key]
	if !ok {
		target = NewHistogram(metric)
		s[key] = target
	}
	target.Merge(h)
}

// IsLatencyMetric reports whether a metric holds durations in microseconds.
func IsLatencyMetric(metric string) bool {
	return metric != protocol.MetricThroughput
}

// PhaseName returns the phase name of a "phase:<name>" metric, or "" if the
// metric is not a phase.
func PhaseName(metric string) string {
	name, found := strings.CutPrefix(metric, protocol.MetricPhasePrefix)
	if !found {
		return ""
	}
	return name
}

// LatencySummaryOf computes millisecond percentiles from a latency histogram.
func LatencySummaryOf(h *hdr.Histogram) orbv1.LatencySummary {
	if h == nil || h.TotalCount() == 0 {
		return orbv1.LatencySummary{}
	}
	return orbv1.LatencySummary{
		MinMillis:  microsToMillis(h.Min()),
		P50Millis:  microsToMillis(h.ValueAtQuantile(50)),
		P90Millis:  microsToMillis(h.ValueAtQuantile(90)),
		P95Millis:  microsToMillis(h.ValueAtQuantile(95)),
		P99Millis:  microsToMillis(h.ValueAtQuantile(99)),
		P999Millis: microsToMillis(h.ValueAtQuantile(99.9)),
		MaxMillis:  microsToMillis(h.Max()),
		MeanMillis: round3(h.Mean() / 1000),
	}
}

// ThroughputPercentilesOf computes percentiles from a throughput histogram.
func ThroughputPercentilesOf(h *hdr.Histogram) orbv1.ThroughputPercentiles {
	if h == nil || h.TotalCount() == 0 {
		return orbv1.ThroughputPercentiles{}
	}
	return orbv1.ThroughputPercentiles{
		P1:  float64(h.ValueAtQuantile(1)),
		P50: float64(h.ValueAtQuantile(50)),
		P99: float64(h.ValueAtQuantile(99)),
		Max: float64(h.Max()),
	}
}

// QuantileMillis returns one latency percentile in milliseconds.
func QuantileMillis(h *hdr.Histogram, quantile float64) float64 {
	if h == nil || h.TotalCount() == 0 {
		return 0
	}
	return microsToMillis(h.ValueAtQuantile(quantile))
}

func microsToMillis(micros int64) float64 {
	return round3(float64(micros) / 1000)
}

// round3 rounds to three decimals, which keeps result objects small and readable.
func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}
