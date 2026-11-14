// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"fmt"
	"sort"
	"sync"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/loadprofile"
	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

// Saturation thresholds (LOAD-9, docs/architecture.md).
const (
	saturationCPU           = 0.90
	saturationSchedDelayMs  = 50.0
	saturationIntervalShare = 0.10
)

// recentIntervals is how many recent intervals are kept for live stats.
const recentIntervals = 4

// Aggregator receives the interval reports of one run. It stores every
// report in SQLite and merges them in memory into the totals needed for the
// result summary and the live view (RES-2).
type Aggregator struct {
	mu        sync.Mutex
	store     *store.Store // nil in some tests
	runUID    string
	namespace string
	runName   string
	agents    int
	interval  time.Duration
	scenarios []*scenarioTotals
	reports   int

	// Saturation counters, outside warm-up (LOAD-9).
	agentIntervals     int
	saturatedIntervals int
	highestCPU         float64
	highestSchedDelay  float64
}

// scenarioTotals are the merged figures of one scenario.
type scenarioTotals struct {
	name    string
	typ     orbv1.ScenarioType
	profile loadprofile.Profile

	measured map[string]*operationTotals // outside warm-up
	warmup   map[string]*operationTotals
	stages   map[int]map[string]*stageTotals

	// measuredIntervals and stageIntervals are the distinct interval numbers
	// seen, used to compute rates.
	measuredIntervals map[int]bool
	stageIntervals    map[int]map[int]bool

	recent map[int]*intervalTotals
}

// operationTotals are the merged figures of one operation type.
type operationTotals struct {
	count        int64
	errors       int64
	bytes        int64
	errorsByType map[string]int64
	counters     map[string]int64
	histograms   metrics.HistogramSet // keyed by metric name
}

type stageTotals struct {
	count   int64
	errors  int64
	latency *hdr.Histogram
}

// intervalTotals are the merged figures of one recent interval, for the
// live view.
type intervalTotals struct {
	agentsReported int
	workers        int
	operations     map[string]*operationTotals
}

// NewAggregator creates the aggregator for a run.
func NewAggregator(st *store.Store, plan protocol.Plan) *Aggregator {
	interval := orbv1.DefaultInterval
	if plan.Spec.Measurement.Interval != nil && plan.Spec.Measurement.Interval.Duration > 0 {
		interval = plan.Spec.Measurement.Interval.Duration
	}
	a := &Aggregator{
		store:     st,
		runUID:    plan.RunUID,
		namespace: plan.Namespace,
		runName:   plan.RunName,
		agents:    plan.Agents,
		interval:  interval,
	}
	for _, sc := range plan.Spec.Scenarios {
		a.scenarios = append(a.scenarios, &scenarioTotals{
			name:              sc.Name,
			typ:               sc.Type,
			profile:           loadprofile.New(sc.Profile),
			measured:          map[string]*operationTotals{},
			warmup:            map[string]*operationTotals{},
			stages:            map[int]map[string]*stageTotals{},
			measuredIntervals: map[int]bool{},
			stageIntervals:    map[int]map[int]bool{},
			recent:            map[int]*intervalTotals{},
		})
	}
	return a
}

// AddReport stores and merges one interval report from an agent.
func (a *Aggregator) AddReport(agent int, r protocol.IntervalReport) error {
	if a.store != nil {
		if err := a.store.InsertReport(a.runUID, agent, r); err != nil {
			return fmt.Errorf("storing report: %w", err)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if r.Scenario < 0 || r.Scenario >= len(a.scenarios) {
		return fmt.Errorf("report names unknown scenario index %d", r.Scenario)
	}
	sc := a.scenarios[r.Scenario]
	a.reports++

	target := sc.measured
	if r.Warmup {
		target = sc.warmup
	} else {
		sc.measuredIntervals[r.Interval] = true
		if sc.stageIntervals[r.Stage] == nil {
			sc.stageIntervals[r.Stage] = map[int]bool{}
		}
		sc.stageIntervals[r.Stage][r.Interval] = true
		a.countSaturation(r)
	}

	live := sc.recentInterval(r.Interval)
	live.agentsReported++
	live.workers += r.Workers

	for _, op := range r.Operations {
		if err := totalsFor(target, op.Operation).add(op, true); err != nil {
			return err
		}
		if err := totalsFor(live.operations, op.Operation).add(op, false); err != nil {
			return err
		}
		if !r.Warmup {
			if err := sc.addStage(r.Stage, op); err != nil {
				return err
			}
		}
		recordPrometheus(a.namespace, a.runName, sc.name, op)
	}
	setActiveWorkers(a.namespace, a.runName, a.liveWorkersLocked())
	return nil
}

func (a *Aggregator) countSaturation(r protocol.IntervalReport) {
	a.agentIntervals++
	a.highestCPU = max(a.highestCPU, r.CPUUtilization)
	a.highestSchedDelay = max(a.highestSchedDelay, r.SchedDelayP99Millis)
	if r.CPUUtilization > saturationCPU || r.SchedDelayP99Millis > saturationSchedDelayMs {
		a.saturatedIntervals++
	}
}

func totalsFor(m map[string]*operationTotals, operation string) *operationTotals {
	t, ok := m[operation]
	if !ok {
		t = &operationTotals{errorsByType: map[string]int64{}, counters: map[string]int64{}, histograms: metrics.HistogramSet{}}
		m[operation] = t
	}
	return t
}

// add merges an operation report. If allMetrics is false, only the latency
// histogram is merged (enough for the live view).
func (t *operationTotals) add(op protocol.OperationReport, allMetrics bool) error {
	t.count += op.Count
	t.bytes += op.Bytes
	for errorType, n := range op.Errors {
		t.errors += n
		t.errorsByType[errorType] += n
	}
	for name, n := range op.Counters {
		t.counters[name] += n
	}
	for metric, data := range op.Histograms {
		if !allMetrics && metric != protocol.MetricLatency {
			continue
		}
		if err := t.histograms.MergeEncoded(metric, metric, data); err != nil {
			return err
		}
	}
	return nil
}

func (sc *scenarioTotals) addStage(stage int, op protocol.OperationReport) error {
	if sc.stages[stage] == nil {
		sc.stages[stage] = map[string]*stageTotals{}
	}
	st, ok := sc.stages[stage][op.Operation]
	if !ok {
		st = &stageTotals{latency: metrics.NewHistogram(protocol.MetricLatency)}
		sc.stages[stage][op.Operation] = st
	}
	st.count += op.Count
	for _, n := range op.Errors {
		st.errors += n
	}
	if data, ok := op.Histograms[protocol.MetricLatency]; ok {
		h, err := metrics.Decode(data)
		if err != nil {
			return err
		}
		st.latency.Merge(h)
	}
	return nil
}

// recentInterval returns the live totals for an interval, dropping the
// oldest intervals so memory stays small.
func (sc *scenarioTotals) recentInterval(interval int) *intervalTotals {
	if t, ok := sc.recent[interval]; ok {
		return t
	}
	t := &intervalTotals{operations: map[string]*operationTotals{}}
	sc.recent[interval] = t
	for len(sc.recent) > recentIntervals {
		oldest := interval
		for i := range sc.recent {
			oldest = min(oldest, i)
		}
		delete(sc.recent, oldest)
	}
	return t
}

// HasData reports whether any report has been received.
func (a *Aggregator) HasData() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reports > 0
}

// Live returns figures from the most recent complete interval: the newest
// interval every agent has reported, or else the newest one seen.
func (a *Aggregator) Live(scenarioIndex int) *orbv1.LiveStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	if scenarioIndex < 0 || scenarioIndex >= len(a.scenarios) {
		return nil
	}
	sc := a.scenarios[scenarioIndex]
	interval, ok := sc.liveInterval(a.agents)
	if !ok {
		return nil
	}
	totals := sc.recent[interval]

	stats := &orbv1.LiveStats{Scenario: sc.name, Workers: totals.workers, UpdatedAt: metav1.Now()}
	seconds := a.interval.Seconds()
	for _, name := range sortedKeys(totals.operations) {
		op := totals.operations[name]
		latency := op.histograms[protocol.MetricLatency]
		stats.Operations = append(stats.Operations, orbv1.LiveOperation{
			Operation:      name,
			OpsPerSecond:   round2(float64(op.count) / seconds),
			P50Millis:      metrics.QuantileMillis(latency, 50),
			P99Millis:      metrics.QuantileMillis(latency, 99),
			BytesPerSecond: round2(float64(op.bytes) / seconds),
			Errors:         op.errors,
		})
		setLiveP99(a.namespace, a.runName, name, metrics.QuantileMillis(latency, 99)/1000)
	}
	return stats
}

func (sc *scenarioTotals) liveInterval(agents int) (int, bool) {
	best, newest := -1, -1
	for i, t := range sc.recent {
		newest = max(newest, i)
		if t.agentsReported >= agents {
			best = max(best, i)
		}
	}
	if best >= 0 {
		return best, true
	}
	return newest, newest >= 0
}

func (a *Aggregator) liveWorkersLocked() int {
	workers := 0
	for _, sc := range a.scenarios {
		if interval, ok := sc.liveInterval(a.agents); ok {
			workers = sc.recent[interval].workers
		}
	}
	return workers
}

// Saturation reports whether the agents were the bottleneck (LOAD-9).
func (a *Aggregator) Saturation() (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.agentIntervals == 0 {
		return false, ""
	}
	share := float64(a.saturatedIntervals) / float64(a.agentIntervals)
	if share <= saturationIntervalShare {
		return false, ""
	}
	return true, fmt.Sprintf(
		"%.0f%% of agent intervals had CPU use above %.0f%% or a scheduler delay p99 above %.0f ms "+
			"(highest CPU %.0f%%, highest scheduler delay %.1f ms); add agents or lower workersPerAgent",
		share*100, saturationCPU*100, saturationSchedDelayMs, a.highestCPU*100, a.highestSchedDelay)
}

// ScenarioResults returns the summary of every scenario, without the time
// series (the result builder adds that from the store).
func (a *Aggregator) ScenarioResults() []orbv1.ScenarioResult {
	a.mu.Lock()
	defer a.mu.Unlock()

	seconds := a.interval.Seconds()
	results := make([]orbv1.ScenarioResult, 0, len(a.scenarios))
	for _, sc := range a.scenarios {
		duration := float64(len(sc.measuredIntervals)) * seconds
		result := orbv1.ScenarioResult{
			Name:            sc.name,
			Type:            sc.typ,
			DurationSeconds: duration,
			Operations:      operationResults(sc.measured, duration),
			Warmup:          operationResults(sc.warmup, sc.profile.Warmup().Seconds()),
		}
		stageWorkers := sc.profile.StageWorkers()
		for _, stage := range sortedIntKeys(sc.stages) {
			stageSeconds := float64(len(sc.stageIntervals[stage])) * seconds
			sr := orbv1.StageResult{Index: stage}
			if stage < len(stageWorkers) {
				sr.Workers = stageWorkers[stage]
			}
			for _, name := range sortedKeys(sc.stages[stage]) {
				st := sc.stages[stage][name]
				sr.Operations = append(sr.Operations, orbv1.StageOperation{
					Operation:    name,
					Count:        st.count,
					Errors:       st.errors,
					OpsPerSecond: rate(st.count, stageSeconds),
					P50Millis:    metrics.QuantileMillis(st.latency, 50),
					P99Millis:    metrics.QuantileMillis(st.latency, 99),
				})
			}
			result.Stages = append(result.Stages, sr)
		}
		results = append(results, result)
	}
	return results
}

// operationResults turns merged totals into result entries (RES-3).
func operationResults(totals map[string]*operationTotals, seconds float64) []orbv1.OperationResult {
	var out []orbv1.OperationResult
	for _, name := range sortedKeys(totals) {
		t := totals[name]
		r := orbv1.OperationResult{
			Operation:    name,
			Count:        t.count,
			Errors:       t.errors,
			OpsPerSecond: rate(t.count, seconds),
			Latency:      metrics.LatencySummaryOf(t.histograms[protocol.MetricLatency]),
		}
		if len(t.errorsByType) > 0 {
			r.ErrorsByType = copyCounts(t.errorsByType)
		}
		if len(t.counters) > 0 {
			r.Counters = copyCounts(t.counters)
		}
		if total, ok := t.histograms[protocol.MetricTotal]; ok {
			summary := metrics.LatencySummaryOf(total)
			r.Total = &summary
		}
		if through, ok := t.histograms[protocol.MetricThroughput]; ok || t.bytes > 0 {
			r.Throughput = &orbv1.ThroughputSummary{
				TotalBytes:              t.bytes,
				AggregateBytesPerSecond: rate(t.bytes, seconds),
				PerOperation:            metrics.ThroughputPercentilesOf(through),
			}
		}
		for metric, h := range t.histograms {
			if phase := metrics.PhaseName(metric); phase != "" {
				if r.Phases == nil {
					r.Phases = map[string]orbv1.LatencySummary{}
				}
				r.Phases[phase] = metrics.LatencySummaryOf(h)
			} else if metric == protocol.MetricBlobStep {
				if r.Phases == nil {
					r.Phases = map[string]orbv1.LatencySummary{}
				}
				r.Phases[protocol.MetricBlobStep] = metrics.LatencySummaryOf(h)
			}
		}
		out = append(out, r)
	}
	return out
}

func rate(count int64, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return round2(float64(count) / seconds)
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func copyCounts(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedIntKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
