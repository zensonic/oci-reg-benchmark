// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"sort"
	"sync"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"

	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// Result is the outcome of one operation, as measured by a worker.
type Result struct {
	// Operation is the operation name, for example "pull".
	Operation string

	// Start is when the operation started.
	Start time.Time

	// Latency is the operation's latency as defined in section 8.
	Latency time.Duration

	// Total is the time for the whole operation.
	Total time.Duration

	// Bytes is the blob data transferred.
	Bytes int64

	// TransferTime is the time from first to last byte, used for
	// per-operation throughput (THR-1). Zero if no data was transferred.
	TransferTime time.Duration

	// Phases are the durations of the operation's phases (LAT-6).
	Phases map[string]time.Duration

	// BlobSteps are the durations of per-blob deduplication steps (DEDUP-5).
	BlobSteps []time.Duration

	// Counters are extra counts, for example "pages" or "mountMisses".
	Counters map[string]int64

	// Err is set if the operation failed. ErrorType classifies it, for
	// example "http_503", "timeout" or "connection".
	Err       error
	ErrorType string
}

// Recorder collects results from all workers of one agent and turns them
// into one report per measurement interval. It is safe for concurrent use.
type Recorder struct {
	mu          sync.Mutex
	operations  map[string]*accumulator
	keepSamples bool
	samples     []protocol.Sample
	origin      time.Time
}

// NewRecorder returns a recorder. Raw samples are kept only if keepSamples
// is true (RES-9). Sample offsets are measured from origin.
func NewRecorder(keepSamples bool, origin time.Time) *Recorder {
	return &Recorder{
		operations:  map[string]*accumulator{},
		keepSamples: keepSamples,
		origin:      origin,
	}
}

// accumulator holds the running figures of one operation type.
type accumulator struct {
	count      int64
	bytes      int64
	errors     map[string]int64
	examples   map[string]string
	counters   map[string]int64
	histograms map[string]*hdr.Histogram
}

func newAccumulator() *accumulator {
	return &accumulator{
		errors:     map[string]int64{},
		examples:   map[string]string{},
		counters:   map[string]int64{},
		histograms: map[string]*hdr.Histogram{},
	}
}

func (a *accumulator) histogram(metric string) *hdr.Histogram {
	h, ok := a.histograms[metric]
	if !ok {
		h = NewHistogram(metric)
		a.histograms[metric] = h
	}
	return h
}

// Record adds one result. Failed operations are counted, but their timings
// are left out of the histograms so they do not distort the percentiles.
func (r *Recorder) Record(res Result) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acc, ok := r.operations[res.Operation]
	if !ok {
		acc = newAccumulator()
		r.operations[res.Operation] = acc
	}

	acc.count++
	acc.bytes += res.Bytes
	for name, value := range res.Counters {
		acc.counters[name] += value
	}

	if r.keepSamples {
		r.samples = append(r.samples, r.sampleOf(res))
	}

	if res.Err != nil {
		errorType := res.ErrorType
		if errorType == "" {
			errorType = "other"
		}
		acc.errors[errorType]++
		if _, seen := acc.examples[errorType]; !seen {
			acc.examples[errorType] = res.Err.Error()
		}
		return
	}

	RecordDuration(acc.histogram(protocol.MetricLatency), res.Latency)
	RecordDuration(acc.histogram(protocol.MetricTotal), res.Total)
	if res.TransferTime > 0 && res.Bytes > 0 {
		bytesPerSecond := float64(res.Bytes) / res.TransferTime.Seconds()
		RecordValue(acc.histogram(protocol.MetricThroughput), int64(bytesPerSecond))
	}
	for phase, d := range res.Phases {
		RecordDuration(acc.histogram(protocol.MetricPhasePrefix+phase), d)
	}
	for _, d := range res.BlobSteps {
		RecordDuration(acc.histogram(protocol.MetricBlobStep), d)
	}
}

func (r *Recorder) sampleOf(res Result) protocol.Sample {
	s := protocol.Sample{
		Operation:     res.Operation,
		OffsetMicros:  res.Start.Sub(r.origin).Microseconds(),
		LatencyMicros: res.Latency.Microseconds(),
		TotalMicros:   res.Total.Microseconds(),
		Bytes:         res.Bytes,
	}
	if res.Err != nil {
		s.Error = res.Err.Error()
	}
	return s
}

// Flush returns the figures collected since the last flush and starts a new
// interval. Operations are sorted by name so reports are stable.
func (r *Recorder) Flush() ([]protocol.OperationReport, []protocol.Sample, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reports := make([]protocol.OperationReport, 0, len(r.operations))
	for name, acc := range r.operations {
		if acc.count == 0 {
			continue
		}
		report, err := acc.report(name)
		if err != nil {
			return nil, nil, err
		}
		reports = append(reports, report)
		acc.reset()
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Operation < reports[j].Operation })

	samples := r.samples
	r.samples = nil
	return reports, samples, nil
}

func (a *accumulator) report(operation string) (protocol.OperationReport, error) {
	report := protocol.OperationReport{
		Operation:     operation,
		Count:         a.count,
		Bytes:         a.bytes,
		Errors:        copyMap(a.errors),
		ErrorExamples: copyMap(a.examples),
		Counters:      copyMap(a.counters),
		Histograms:    map[string][]byte{},
	}
	for metric, h := range a.histograms {
		if h.TotalCount() == 0 {
			continue
		}
		data, err := Encode(h)
		if err != nil {
			return report, err
		}
		report.Histograms[metric] = data
	}
	return report, nil
}

// reset clears the accumulator but keeps the allocated histograms for reuse.
func (a *accumulator) reset() {
	a.count, a.bytes = 0, 0
	clear(a.errors)
	clear(a.examples)
	clear(a.counters)
	for _, h := range a.histograms {
		h.Reset()
	}
}

// copyMap returns a copy, or nil for an empty map, so empty maps are left out
// of JSON.
func copyMap[V any](m map[string]V) map[string]V {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
