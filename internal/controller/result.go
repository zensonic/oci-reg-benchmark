// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

const (
	// maxTimePoints is the most time series points per scenario in the
	// result object (RES-4).
	maxTimePoints = 120

	// resultSizeLimit keeps the result object well under the ~1.5 MiB etcd
	// limit (RES-5).
	resultSizeLimit = 800 * 1024
)

// resultInput is everything the result builder needs.
type resultInput struct {
	Run         *orbv1.OrbBenchmarkRun
	Spec        *orbv1.OrbBenchmarkSpec
	Snapshot    Snapshot
	Aggregator  *Aggregator
	Store       *store.Store
	Agents      int
	CompletedAt time.Time
}

// buildResult builds the OrbBenchmarkResult for a run (section 11.1).
func buildResult(in resultInput) (*orbv1.OrbBenchmarkResult, error) {
	run := in.Run
	summary := orbv1.ResultSummary{
		RunName:       run.Name,
		RunUID:        string(run.UID),
		BenchmarkName: run.Spec.BenchmarkRef,
		Aborted:       in.Snapshot.Aborted,
		CompletedAt:   metav1.NewTime(in.CompletedAt),
		Registry: orbv1.RegistryInfo{
			URL:     in.Spec.Registry.URL,
			Module:  in.Spec.Registry.Module,
			Version: in.Snapshot.RegistryVersion,
		},
		Agents:        in.Agents,
		Detail:        orbv1.DetailReference{Store: "sqlite", RunUID: string(run.UID)},
		BenchmarkSpec: *in.Spec.DeepCopy(),
		Images:        in.Snapshot.ResolvedImages,
		Cleanup:       in.Snapshot.Cleanup,
	}
	if run.Status.StartedAt != nil {
		summary.StartedAt = *run.Status.StartedAt
	}
	if in.Aggregator != nil {
		summary.Saturated, summary.SaturationReason = in.Aggregator.Saturation()
		summary.Scenarios = in.Aggregator.ScenarioResults()
	}

	result := &orbv1.OrbBenchmarkResult{
		TypeMeta: metav1.TypeMeta{APIVersion: orbv1.GroupVersion.String(), Kind: "OrbBenchmarkResult"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      run.Name,
			Namespace: run.Namespace,
			Labels: map[string]string{
				LabelRun:       run.Name,
				LabelBenchmark: run.Spec.BenchmarkRef,
			},
		},
		Summary: summary,
	}

	if in.Store != nil {
		if err := addTimeSeries(result, in.Store, intervalOf(in.Spec)); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// addTimeSeries adds a coarse time series per scenario. The buckets start at
// a width that gives at most maxTimePoints points, and are widened until the
// whole object fits under resultSizeLimit (RES-5). If even very wide buckets
// do not fit, the time series is left out; the detail data in SQLite still
// has full resolution.
func addTimeSeries(result *orbv1.OrbBenchmarkResult, st *store.Store, interval time.Duration) error {
	scenarios := result.Summary.Scenarios
	widthFactor := 1
	for {
		for i := range scenarios {
			lastInterval, err := lastIntervalOf(st, result.Summary.RunUID, scenarios[i].Name)
			if err != nil {
				return err
			}
			if lastInterval < 0 {
				continue
			}
			total := float64(lastInterval+1) * interval.Seconds()
			bucket := int(math.Ceil(total/maxTimePoints)) * widthFactor
			bucket = max(bucket, int(math.Ceil(interval.Seconds())))
			series, err := timeSeries(st, result.Summary.RunUID, scenarios[i].Name, interval, bucket)
			if err != nil {
				return err
			}
			scenarios[i].TimeSeries = series
		}

		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if len(data) <= resultSizeLimit {
			return nil
		}
		widthFactor *= 2
		if widthFactor > 64 {
			for i := range scenarios {
				scenarios[i].TimeSeries = nil
			}
			return nil
		}
	}
}

func lastIntervalOf(st *store.Store, runUID, scenario string) (int, error) {
	rows, err := st.Intervals(runUID, scenario)
	if err != nil {
		return -1, err
	}
	last := -1
	for _, r := range rows {
		last = max(last, r.Interval)
	}
	return last, nil
}

// bucketTotals are the figures of one time series bucket.
type bucketTotals struct {
	intervals  map[int]int // interval -> workers summed across agents
	operations map[string]*bucketOperation
}

type bucketOperation struct {
	count, errors, bytes int64
	latency              metrics.HistogramSet
}

// timeSeries builds a time series with buckets of bucketSeconds width.
func timeSeries(st *store.Store, runUID, scenario string, interval time.Duration, bucketSeconds int) (*orbv1.TimeSeries, error) {
	bucketOf := func(i int) int {
		return int(float64(i) * interval.Seconds() / float64(bucketSeconds))
	}
	buckets := map[int]*bucketTotals{}
	get := func(i int) *bucketTotals {
		b, ok := buckets[bucketOf(i)]
		if !ok {
			b = &bucketTotals{intervals: map[int]int{}, operations: map[string]*bucketOperation{}}
			buckets[bucketOf(i)] = b
		}
		return b
	}
	operation := func(b *bucketTotals, name string) *bucketOperation {
		op, ok := b.operations[name]
		if !ok {
			op = &bucketOperation{latency: metrics.HistogramSet{}}
			b.operations[name] = op
		}
		return op
	}

	intervals, err := st.Intervals(runUID, scenario)
	if err != nil {
		return nil, err
	}
	for _, r := range intervals {
		get(r.Interval).intervals[r.Interval] += r.Workers
	}
	operations, err := st.Operations(runUID, scenario)
	if err != nil {
		return nil, err
	}
	for _, r := range operations {
		op := operation(get(r.Interval), r.Operation)
		op.count += r.Count
		op.errors += r.Errors
		op.bytes += r.Bytes
	}
	err = st.EachHistogram(runUID, scenario, protocol.MetricLatency, func(r store.HistogramRow) error {
		return operation(get(r.Interval), r.Operation).latency.MergeEncoded(r.Operation, r.Metric, r.Data)
	})
	if err != nil {
		return nil, err
	}

	series := &orbv1.TimeSeries{BucketSeconds: bucketSeconds}
	for _, index := range sortedIntKeys(buckets) {
		b := buckets[index]
		seconds := float64(len(b.intervals)) * interval.Seconds()
		workers := 0
		for _, w := range b.intervals {
			workers += w
		}
		point := orbv1.TimePoint{
			OffsetSeconds: index * bucketSeconds,
			Workers:       int(math.Round(float64(workers) / float64(max(len(b.intervals), 1)))),
		}
		names := make([]string, 0, len(b.operations))
		for name := range b.operations {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			op := b.operations[name]
			latency := op.latency[name]
			point.Operations = append(point.Operations, orbv1.TimePointOperation{
				Operation:      name,
				OpsPerSecond:   rate(op.count, seconds),
				P50Millis:      metrics.QuantileMillis(latency, 50),
				P99Millis:      metrics.QuantileMillis(latency, 99),
				BytesPerSecond: rate(op.bytes, seconds),
				Errors:         op.errors,
			})
		}
		series.Points = append(series.Points, point)
	}
	return series, nil
}

func intervalOf(spec *orbv1.OrbBenchmarkSpec) time.Duration {
	if spec.Measurement.Interval != nil && spec.Measurement.Interval.Duration > 0 {
		return spec.Measurement.Interval.Duration
	}
	return orbv1.DefaultInterval
}
