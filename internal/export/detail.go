// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package export turns a run's SQLite detail data into protocol.Detail and
// serves it on the controller's export endpoint (RES-13, RES-14).
package export

import (
	"sort"

	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

// BuildDetail reads a run's detail data from the store.
func BuildDetail(st *store.Store, runUID string) (*protocol.Detail, error) {
	scenarios, err := st.Scenarios(runUID)
	if err != nil {
		return nil, err
	}
	detail := &protocol.Detail{RunUID: runUID, Scenarios: []protocol.ScenarioDetail{}}
	for _, name := range scenarios {
		sd, err := buildScenario(st, runUID, name)
		if err != nil {
			return nil, err
		}
		detail.Scenarios = append(detail.Scenarios, *sd)
	}
	return detail, nil
}

func buildScenario(st *store.Store, runUID, scenario string) (*protocol.ScenarioDetail, error) {
	intervalRows, err := st.Intervals(runUID, scenario)
	if err != nil {
		return nil, err
	}
	operationRows, err := st.Operations(runUID, scenario)
	if err != nil {
		return nil, err
	}

	sd := &protocol.ScenarioDetail{Name: scenario}

	// Interval figures merged across agents, plus each agent's own figures.
	byInterval := map[int]*protocol.IntervalDetail{}
	warmup := map[int]bool{}
	var order []int
	for _, r := range intervalRows {
		d, ok := byInterval[r.Interval]
		if !ok {
			d = &protocol.IntervalDetail{Interval: r.Interval, Stage: r.Stage}
			byInterval[r.Interval] = d
			order = append(order, r.Interval)
		}
		d.Workers += r.Workers
		d.Warmup = d.Warmup || r.Warmup
		warmup[r.Interval] = d.Warmup
		sd.Agents = append(sd.Agents, protocol.AgentInterval{
			Agent: r.Agent, Interval: r.Interval, Workers: r.Workers,
			CPUUtilization: r.CPUUtilization, SchedDelayP99Millis: r.SchedDelayP99Millis,
		})
	}

	// Counts per interval and operation.
	type opKey struct {
		interval  int
		operation string
	}
	counts := map[opKey]*protocol.IntervalOperation{}
	for _, r := range operationRows {
		key := opKey{r.Interval, r.Operation}
		op, ok := counts[key]
		if !ok {
			op = &protocol.IntervalOperation{Operation: r.Operation}
			counts[key] = op
		}
		op.Count += r.Count
		op.Errors += r.Errors
		op.Bytes += r.Bytes
	}

	// Histograms arrive ordered by interval, so only one interval's latency
	// histograms are held in memory at a time. Phase summaries cover the
	// whole scenario, excluding warm-up.
	phases := metrics.HistogramSet{}
	phaseKeys := map[string][2]string{}
	current := -1
	intervalLatency := metrics.HistogramSet{}
	flush := func() {
		for operation, h := range intervalLatency {
			if op, ok := counts[opKey{current, operation}]; ok {
				op.P50Millis = metrics.QuantileMillis(h, 50)
				op.P90Millis = metrics.QuantileMillis(h, 90)
				op.P99Millis = metrics.QuantileMillis(h, 99)
				op.MaxMillis = metrics.QuantileMillis(h, 100)
			}
		}
		intervalLatency = metrics.HistogramSet{}
	}
	err = st.EachHistogram(runUID, scenario, "", func(r store.HistogramRow) error {
		if r.Interval != current {
			flush()
			current = r.Interval
		}
		h, err := metrics.Decode(r.Data)
		if err != nil {
			return err
		}
		if r.Metric == protocol.MetricLatency {
			intervalLatency.Merge(r.Operation, r.Metric, h)
		}
		if !warmup[r.Interval] && metrics.IsLatencyMetric(r.Metric) {
			key := r.Operation + "\x00" + r.Metric
			phases.Merge(key, r.Metric, h)
			phaseKeys[key] = [2]string{r.Operation, r.Metric}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	flush()

	for _, interval := range order {
		d := byInterval[interval]
		for key, op := range counts {
			if key.interval == interval {
				d.Operations = append(d.Operations, *op)
			}
		}
		sort.Slice(d.Operations, func(i, j int) bool { return d.Operations[i].Operation < d.Operations[j].Operation })
		sd.Intervals = append(sd.Intervals, *d)
	}

	for key, h := range phases {
		names := phaseKeys[key]
		sd.Phases = append(sd.Phases, protocol.PhaseDetail{
			Operation: names[0], Metric: names[1], Count: h.TotalCount(), Latency: metrics.LatencySummaryOf(h),
		})
	}
	sort.Slice(sd.Phases, func(i, j int) bool {
		if sd.Phases[i].Operation != sd.Phases[j].Operation {
			return sd.Phases[i].Operation < sd.Phases[j].Operation
		}
		return sd.Phases[i].Metric < sd.Phases[j].Metric
	})

	sd.Errors, err = buildErrors(st, runUID, scenario)
	if err != nil {
		return nil, err
	}
	sd.Samples, err = st.Samples(runUID, scenario)
	if err != nil {
		return nil, err
	}
	if sd.Intervals == nil {
		sd.Intervals = []protocol.IntervalDetail{}
	}
	return sd, nil
}

func buildErrors(st *store.Store, runUID, scenario string) ([]protocol.ErrorDetail, error) {
	counters, err := st.Counters(runUID, scenario)
	if err != nil {
		return nil, err
	}
	examples, err := st.ErrorExamples(runUID, scenario)
	if err != nil {
		return nil, err
	}
	exampleOf := map[[2]string]string{}
	for _, e := range examples {
		exampleOf[[2]string{e.Operation, e.Type}] = e.Message
	}

	totals := map[[2]string]int64{}
	for _, c := range counters {
		if c.Kind == store.CounterKindError {
			totals[[2]string{c.Operation, c.Name}] += c.Value
		}
	}
	out := make([]protocol.ErrorDetail, 0, len(totals))
	for key, count := range totals {
		out = append(out, protocol.ErrorDetail{Operation: key[0], Type: key[1], Count: count, Example: exampleOf[key]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Operation != out[j].Operation {
			return out[i].Operation < out[j].Operation
		}
		return out[i].Type < out[j].Type
	})
	return out, nil
}
