// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

func newRunCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "run",
		Aliases: []string{"runs"},
		Short:   "Start and follow benchmark runs (OrbBenchmarkRun)",
	}
	cmd.AddCommand(
		newRunStartCommand(o),
		newRunListCommand(o),
		newRunStatusCommand(o),
		newRunWatchCommand(o),
		newRunAbortCommand(o),
	)
	return cmd
}

func newRunStartCommand(o *options) *cobra.Command {
	var (
		name     string
		settings []string
		watch    bool
		format   string
	)
	cmd := &cobra.Command{
		Use:   "start BENCHMARK",
		Short: "Start a new run of a benchmark",
		Long: "Start a new run of a benchmark. Each run is kept with its own results, so the\n" +
			"same benchmark can be run again and compared.\n\n" + overrideHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format, formatTable, "name"); err != nil {
				return err
			}
			overrides, err := parseOverrides(settings)
			if err != nil {
				return err
			}
			k, err := o.client()
			if err != nil {
				return err
			}

			// Fail early with a clear message if the benchmark does not exist.
			var benchmark orbv1.OrbBenchmark
			if err := k.client.Get(cmd.Context(), client.ObjectKey{Namespace: k.namespace, Name: args[0]}, &benchmark); err != nil {
				return fmt.Errorf("reading orbbenchmark/%s: %w", args[0], err)
			}

			run := &orbv1.OrbBenchmarkRun{
				ObjectMeta: metav1.ObjectMeta{Namespace: k.namespace, Name: name},
				Spec:       orbv1.OrbBenchmarkRunSpec{BenchmarkRef: args[0], Overrides: overrides},
			}
			if name == "" {
				run.GenerateName = args[0] + "-"
			}
			if err := k.client.Create(cmd.Context(), run); err != nil {
				return fmt.Errorf("creating the run: %w", err)
			}

			if format == "name" {
				fmt.Fprintln(o.out, run.Name)
			} else {
				fmt.Fprintf(o.out, "orbbenchmarkrun/%s created\n", run.Name)
			}
			if watch {
				return watchRun(cmd, o, k, run.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name of the run (default: <benchmark>-<random suffix>)")
	cmd.Flags().StringArrayVar(&settings, "set", nil, "override a benchmark field for this run, as key=value (repeatable)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "follow the run until it finishes")
	cmd.Flags().StringVarP(&format, "output", "o", formatTable, `output format: table, or "name" to print only the run name`)
	return cmd
}

func newRunListCommand(o *options) *cobra.Command {
	var (
		benchmark string
		format    string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List runs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format, formatTable, formatJSON, formatYAML); err != nil {
				return err
			}
			k, err := o.client()
			if err != nil {
				return err
			}
			var list orbv1.OrbBenchmarkRunList
			if err := k.client.List(cmd.Context(), &list, client.InNamespace(k.namespace)); err != nil {
				return fmt.Errorf("listing runs: %w", err)
			}
			runs := list.Items[:0]
			for _, r := range list.Items {
				if benchmark == "" || r.Spec.BenchmarkRef == benchmark {
					runs = append(runs, r)
				}
			}
			sort.Slice(runs, func(i, j int) bool {
				return runs[i].CreationTimestamp.After(runs[j].CreationTimestamp.Time)
			})
			list.Items = runs

			if format != formatTable {
				return printStructured(o.out, format, list)
			}
			if len(runs) == 0 {
				fmt.Fprintf(o.out, "No runs in namespace %s.\n", k.namespace)
				return nil
			}
			t := newTable(o.out, "NAME", "BENCHMARK", "PHASE", "SCENARIO", "AGENTS", "AGE")
			for _, r := range runs {
				t.row(r.Name, r.Spec.BenchmarkRef, orDash(string(r.Status.Phase)), orDash(r.Status.ActiveScenario),
					r.Status.Agents, age(r.CreationTimestamp.Time))
			}
			return t.flush()
		},
	}
	cmd.Flags().StringVar(&benchmark, "benchmark", "", "only list runs of this benchmark")
	cmd.Flags().StringVarP(&format, "output", "o", formatTable, "output format: table, json or yaml")
	return cmd
}

func newRunStatusCommand(o *options) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "status RUN",
		Short: "Show the status of a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format, formatTable, formatJSON, formatYAML); err != nil {
				return err
			}
			k, err := o.client()
			if err != nil {
				return err
			}
			run, err := getRun(cmd, k, args[0])
			if err != nil {
				return err
			}
			if format != formatTable {
				run.ManagedFields = nil
				return printStructured(o.out, format, run)
			}
			return printRunStatus(o.out, run)
		},
	}
	cmd.Flags().StringVarP(&format, "output", "o", formatTable, "output format: table, json or yaml")
	return cmd
}

func getRun(cmd *cobra.Command, k *kube, name string) (*orbv1.OrbBenchmarkRun, error) {
	var run orbv1.OrbBenchmarkRun
	if err := k.client.Get(cmd.Context(), client.ObjectKey{Namespace: k.namespace, Name: name}, &run); err != nil {
		return nil, fmt.Errorf("reading orbbenchmarkrun/%s: %w", name, err)
	}
	return &run, nil
}

// printRunStatus prints a human-readable status of a run.
func printRunStatus(w io.Writer, run *orbv1.OrbBenchmarkRun) error {
	s := run.Status
	fmt.Fprintf(w, "Run:         %s\n", run.Name)
	fmt.Fprintf(w, "Benchmark:   %s\n", run.Spec.BenchmarkRef)
	fmt.Fprintf(w, "Phase:       %s\n", orDash(string(s.Phase)))
	if s.Message != "" {
		fmt.Fprintf(w, "Message:     %s\n", s.Message)
	}
	if s.ActiveScenario != "" {
		fmt.Fprintf(w, "Scenario:    %s\n", s.ActiveScenario)
	}
	fmt.Fprintf(w, "Agents:      %d ready of %d\n", s.AgentsReady, s.Agents)
	if s.StartedAt != nil {
		fmt.Fprintf(w, "Started:     %s\n", s.StartedAt.Format(time.RFC3339))
	}
	if s.CompletedAt != nil {
		fmt.Fprintf(w, "Completed:   %s\n", s.CompletedAt.Format(time.RFC3339))
	}
	if s.RegistryVersion != "" {
		fmt.Fprintf(w, "Registry:    version %s\n", s.RegistryVersion)
	}
	if s.Cleanup != "" {
		fmt.Fprintf(w, "Cleanup:     %s\n", s.Cleanup)
	}
	if s.ResultName != "" {
		fmt.Fprintf(w, "Result:      %s (orbctl result get %s)\n", s.ResultName, run.Name)
	}

	if len(s.Checks) > 0 {
		fmt.Fprintln(w)
		if err := printChecks(w, s.Checks); err != nil {
			return err
		}
	}
	if s.Live != nil {
		fmt.Fprintln(w)
		if err := printLive(w, s.Live); err != nil {
			return err
		}
	}
	return nil
}

// printChecks prints registry check results.
func printChecks(w io.Writer, checks []orbv1.CheckResult) error {
	t := newTable(w, "CHECK", "RESULT", "MESSAGE")
	for _, c := range checks {
		result := "passed"
		if !c.Passed {
			result = "FAILED"
		}
		t.row(c.Name, result, orDash(c.Message))
	}
	return t.flush()
}

// printLive prints the live figures of the most recent interval.
func printLive(w io.Writer, live *orbv1.LiveStats) error {
	fmt.Fprintf(w, "Scenario %s, %d workers (updated %s ago)\n", live.Scenario, live.Workers, age(live.UpdatedAt.Time))
	t := newTable(w, "OPERATION", "OPS/S", "P50 MS", "P99 MS", "MB/S", "ERRORS")
	for _, op := range live.Operations {
		t.row(op.Operation, rate(op.OpsPerSecond), millis(op.P50Millis), millis(op.P99Millis),
			megabytes(op.BytesPerSecond), op.Errors)
	}
	return t.flush()
}

func newRunAbortCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "abort RUN",
		Short: "Abort a run; results gathered so far are kept",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := o.client()
			if err != nil {
				return err
			}
			run, err := getRun(cmd, k, args[0])
			if err != nil {
				return err
			}
			if run.Status.Phase.IsFinished() {
				return fmt.Errorf("orbbenchmarkrun/%s has already finished (%s)", run.Name, run.Status.Phase)
			}
			patch := client.MergeFrom(run.DeepCopy())
			run.Spec.Abort = true
			if err := k.client.Patch(cmd.Context(), run, patch); err != nil {
				return fmt.Errorf("aborting orbbenchmarkrun/%s: %w", run.Name, err)
			}
			fmt.Fprintf(o.out, "orbbenchmarkrun/%s is being aborted\n", run.Name)
			return nil
		},
	}
}
