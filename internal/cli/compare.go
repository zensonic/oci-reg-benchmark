// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

func newResultCompareCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "compare RUN-A RUN-B",
		Short: "Compare the results of two runs side by side",
		Long: "Compare two runs per scenario and operation: p50 and p99 latency, operations per\n" +
			"second and throughput, with the change from A to B in percent.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := o.client()
			if err != nil {
				return err
			}
			a, err := getResult(cmd, k, args[0])
			if err != nil {
				return err
			}
			b, err := getResult(cmd, k, args[1])
			if err != nil {
				return err
			}
			return printComparison(o.out, &a.Summary, &b.Summary)
		},
	}
}

// printComparison prints scenarios and operations that appear in either run.
func printComparison(w io.Writer, a, b *orbv1.ResultSummary) error {
	fmt.Fprintf(w, "A: %s (benchmark %s, %d agents)\n", a.RunName, a.BenchmarkName, a.Agents)
	fmt.Fprintf(w, "B: %s (benchmark %s, %d agents)\n", b.RunName, b.BenchmarkName, b.Agents)
	if a.Saturated || b.Saturated {
		fmt.Fprintln(w, "Warning: at least one run was saturated; its agents may have limited the results.")
	}

	for _, name := range scenarioUnion(a, b) {
		scA, scB := findScenario(a, name), findScenario(b, name)
		fmt.Fprintf(w, "\nScenario %s\n", name)
		t := newTable(w, "OPERATION",
			"P50 A", "P50 B", "CHANGE",
			"P99 A", "P99 B", "CHANGE",
			"OPS/S A", "OPS/S B", "CHANGE",
			"MB/S A", "MB/S B", "CHANGE")
		for _, op := range operationUnion(scA, scB) {
			opA, opB := findOperation(scA, op), findOperation(scB, op)
			t.row(op,
				millis(opA.Latency.P50Millis), millis(opB.Latency.P50Millis), percentChange(opA.Latency.P50Millis, opB.Latency.P50Millis),
				millis(opA.Latency.P99Millis), millis(opB.Latency.P99Millis), percentChange(opA.Latency.P99Millis, opB.Latency.P99Millis),
				rate(opA.OpsPerSecond), rate(opB.OpsPerSecond), percentChange(opA.OpsPerSecond, opB.OpsPerSecond),
				megabytes(throughputOf(opA)), megabytes(throughputOf(opB)), percentChange(throughputOf(opA), throughputOf(opB)))
		}
		if err := t.flush(); err != nil {
			return err
		}
	}
	return nil
}

func scenarioUnion(a, b *orbv1.ResultSummary) []string {
	var names []string
	seen := map[string]bool{}
	for _, s := range append(append([]orbv1.ScenarioResult{}, a.Scenarios...), b.Scenarios...) {
		if !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	return names
}

func findScenario(s *orbv1.ResultSummary, name string) *orbv1.ScenarioResult {
	for i := range s.Scenarios {
		if s.Scenarios[i].Name == name {
			return &s.Scenarios[i]
		}
	}
	return nil
}

func operationUnion(a, b *orbv1.ScenarioResult) []string {
	var ops []string
	seen := map[string]bool{}
	for _, sc := range []*orbv1.ScenarioResult{a, b} {
		if sc == nil {
			continue
		}
		for _, op := range sc.Operations {
			if !seen[op.Operation] {
				seen[op.Operation] = true
				ops = append(ops, op.Operation)
			}
		}
	}
	return ops
}

// findOperation returns the operation's figures, or zero figures if the
// scenario or operation is missing from one of the runs.
func findOperation(sc *orbv1.ScenarioResult, op string) orbv1.OperationResult {
	if sc != nil {
		for _, o := range sc.Operations {
			if o.Operation == op {
				return o
			}
		}
	}
	return orbv1.OperationResult{Operation: op}
}

func throughputOf(op orbv1.OperationResult) float64 {
	if op.Throughput == nil {
		return 0
	}
	return op.Throughput.AggregateBytesPerSecond
}
