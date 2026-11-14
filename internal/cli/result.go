// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

func newResultCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "result",
		Aliases: []string{"results"},
		Short:   "Show, export and compare run results (OrbBenchmarkResult)",
	}
	cmd.AddCommand(
		newResultGetCommand(o),
		newResultExportCommand(o),
		newResultCompareCommand(o),
	)
	return cmd
}

func newResultGetCommand(o *options) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "get RUN",
		Short: "Show the results of a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format, formatTable, formatJSON, formatYAML); err != nil {
				return err
			}
			k, err := o.client()
			if err != nil {
				return err
			}
			result, err := getResult(cmd, k, args[0])
			if err != nil {
				return err
			}
			if format != formatTable {
				result.ManagedFields = nil
				return printStructured(o.out, format, result)
			}
			return printResult(o.out, &result.Summary)
		},
	}
	cmd.Flags().StringVarP(&format, "output", "o", formatTable, "output format: table, json or yaml")
	return cmd
}

// getResult finds the OrbBenchmarkResult of a run. The run's status names
// it; by convention it has the same name as the run.
func getResult(cmd *cobra.Command, k *kube, runName string) (*orbv1.OrbBenchmarkResult, error) {
	resultName := runName
	var run orbv1.OrbBenchmarkRun
	if err := k.client.Get(cmd.Context(), client.ObjectKey{Namespace: k.namespace, Name: runName}, &run); err == nil {
		if run.Status.ResultName != "" {
			resultName = run.Status.ResultName
		} else if !run.Status.Phase.IsFinished() {
			return nil, fmt.Errorf("orbbenchmarkrun/%s has no results yet (phase %s)", runName, orDash(string(run.Status.Phase)))
		}
	}

	var result orbv1.OrbBenchmarkResult
	if err := k.client.Get(cmd.Context(), client.ObjectKey{Namespace: k.namespace, Name: resultName}, &result); err != nil {
		return nil, fmt.Errorf("reading orbbenchmarkresult/%s: %w", resultName, err)
	}
	return &result, nil
}

// printResult prints a human-readable result summary.
func printResult(w io.Writer, s *orbv1.ResultSummary) error {
	fmt.Fprintf(w, "Run:         %s\n", s.RunName)
	fmt.Fprintf(w, "Benchmark:   %s\n", s.BenchmarkName)
	registry := fmt.Sprintf("%s (module %s", s.Registry.URL, orDash(s.Registry.Module))
	if s.Registry.Version != "" {
		registry += ", version " + s.Registry.Version
	}
	fmt.Fprintf(w, "Registry:    %s)\n", registry)
	fmt.Fprintf(w, "Agents:      %d\n", s.Agents)
	fmt.Fprintf(w, "Started:     %s\n", s.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "Completed:   %s\n", s.CompletedAt.Format(time.RFC3339))
	if s.Aborted {
		fmt.Fprintln(w, "Aborted:     yes, results cover only the part that ran")
	}
	if s.Saturated {
		fmt.Fprintf(w, "Saturated:   yes, the agents were a bottleneck: %s\n", orDash(s.SaturationReason))
	} else {
		fmt.Fprintln(w, "Saturated:   no")
	}
	if s.Cleanup != "" {
		fmt.Fprintf(w, "Cleanup:     %s\n", s.Cleanup)
	}

	for _, sc := range s.Scenarios {
		fmt.Fprintf(w, "\nScenario %s (%s), %.0fs measured\n", sc.Name, sc.Type, sc.DurationSeconds)
		if err := printOperations(w, sc.Operations); err != nil {
			return err
		}
	}
	return nil
}

// printOperations prints the per-operation table of a scenario (RES-3).
func printOperations(w io.Writer, ops []orbv1.OperationResult) error {
	if len(ops) == 0 {
		fmt.Fprintln(w, "  no operations were measured")
		return nil
	}
	t := newTable(w, "OPERATION", "COUNT", "ERRORS", "OPS/S", "P50 MS", "P90 MS", "P99 MS", "MB/S")
	for _, op := range ops {
		throughput := 0.0
		if op.Throughput != nil {
			throughput = op.Throughput.AggregateBytesPerSecond
		}
		t.row(op.Operation, op.Count, op.Errors, rate(op.OpsPerSecond),
			millis(op.Latency.P50Millis), millis(op.Latency.P90Millis), millis(op.Latency.P99Millis),
			megabytes(throughput))
	}
	return t.flush()
}
