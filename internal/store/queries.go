// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// IntervalRow is one agent's interval.
type IntervalRow struct {
	Interval            int
	Agent               int
	Stage               int
	Warmup              bool
	Workers             int
	CPUUtilization      float64
	SchedDelayP99Millis float64
}

// OperationRow are one agent's figures for one operation type in one interval.
type OperationRow struct {
	Interval  int
	Agent     int
	Operation string
	Count     int64
	Errors    int64
	Bytes     int64
}

// HistogramRow is one encoded histogram.
type HistogramRow struct {
	Interval  int
	Agent     int
	Operation string
	Metric    string
	Data      []byte
}

// CounterRow is one error type count or extra counter.
type CounterRow struct {
	Interval  int
	Agent     int
	Operation string
	Kind      string
	Name      string
	Value     int64
}

// ErrorExample is one example message for an error type.
type ErrorExample struct {
	Operation string
	Type      string
	Message   string
}

// Scenarios returns the names of the scenarios of a run, in the order they
// were first reported.
func (s *Store) Scenarios(runUID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT scenario FROM intervals WHERE run_uid = ? GROUP BY scenario ORDER BY MIN(rowid)`, runUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Intervals returns all agent intervals of a scenario, ordered by interval
// and agent.
func (s *Store) Intervals(runUID, scenario string) ([]IntervalRow, error) {
	rows, err := s.db.Query(
		`SELECT interval, agent, stage, warmup, workers, cpu, sched_delay_p99_ms
		 FROM intervals WHERE run_uid = ? AND scenario = ? ORDER BY interval, agent`, runUID, scenario)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IntervalRow
	for rows.Next() {
		var r IntervalRow
		if err := rows.Scan(&r.Interval, &r.Agent, &r.Stage, &r.Warmup, &r.Workers, &r.CPUUtilization, &r.SchedDelayP99Millis); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Operations returns all operation rows of a scenario, ordered by interval.
func (s *Store) Operations(runUID, scenario string) ([]OperationRow, error) {
	rows, err := s.db.Query(
		`SELECT interval, agent, operation, count, errors, bytes
		 FROM operations WHERE run_uid = ? AND scenario = ? ORDER BY interval, operation, agent`, runUID, scenario)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OperationRow
	for rows.Next() {
		var r OperationRow
		if err := rows.Scan(&r.Interval, &r.Agent, &r.Operation, &r.Count, &r.Errors, &r.Bytes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EachHistogram calls fn for every histogram of a scenario, ordered by
// interval. If metric is not empty, only that metric is returned.
// Histograms are streamed rather than loaded at once, because a long run
// has many of them. fn must not use the store: the store has a single
// connection, which is busy until EachHistogram returns.
func (s *Store) EachHistogram(runUID, scenario, metric string, fn func(HistogramRow) error) error {
	query := `SELECT interval, agent, operation, metric, data FROM histograms
	          WHERE run_uid = ? AND scenario = ?`
	args := []any{runUID, scenario}
	if metric != "" {
		query += ` AND metric = ?`
		args = append(args, metric)
	}
	query += ` ORDER BY interval, operation, metric, agent`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var r HistogramRow
		if err := rows.Scan(&r.Interval, &r.Agent, &r.Operation, &r.Metric, &r.Data); err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Counters returns all error counts and extra counters of a scenario.
func (s *Store) Counters(runUID, scenario string) ([]CounterRow, error) {
	rows, err := s.db.Query(
		`SELECT interval, agent, operation, kind, name, value
		 FROM counters WHERE run_uid = ? AND scenario = ? ORDER BY interval, operation`, runUID, scenario)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CounterRow
	for rows.Next() {
		var r CounterRow
		if err := rows.Scan(&r.Interval, &r.Agent, &r.Operation, &r.Kind, &r.Name, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrorExamples returns one example message per operation and error type.
func (s *Store) ErrorExamples(runUID, scenario string) ([]ErrorExample, error) {
	rows, err := s.db.Query(
		`SELECT operation, type, message FROM error_examples
		 WHERE run_uid = ? AND scenario = ? ORDER BY operation, type`, runUID, scenario)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ErrorExample
	for rows.Next() {
		var e ErrorExample
		if err := rows.Scan(&e.Operation, &e.Type, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Samples returns the raw samples of a scenario (RES-9).
func (s *Store) Samples(runUID, scenario string) ([]protocol.AgentSample, error) {
	rows, err := s.db.Query(
		`SELECT agent, operation, offset_us, latency_us, total_us, bytes, error
		 FROM samples WHERE run_uid = ? AND scenario = ? ORDER BY offset_us`, runUID, scenario)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.AgentSample
	for rows.Next() {
		var a protocol.AgentSample
		if err := rows.Scan(&a.Agent, &a.Operation, &a.OffsetMicros, &a.LatencyMicros, &a.TotalMicros, &a.Bytes, &a.Error); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
