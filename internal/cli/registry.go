// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

func newRegistryCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Check the registry of a benchmark",
	}
	cmd.AddCommand(newRegistryCheckCommand(o))
	return cmd
}

func newRegistryCheckCommand(o *options) *cobra.Command {
	var (
		timeout time.Duration
		keep    bool
	)
	cmd := &cobra.Command{
		Use:   "check BENCHMARK",
		Short: "Check connectivity, authentication and supported operations, without load",
		Long: "Start a check-only run of a benchmark. One agent connects to the registry from inside\n" +
			"the cluster, checks the credentials and the operations the benchmark uses, and\n" +
			"reports the results. No load is generated. Exits with a non-zero status if a check fails.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := o.client()
			if err != nil {
				return err
			}

			run := &orbv1.OrbBenchmarkRun{
				ObjectMeta: metav1.ObjectMeta{Namespace: k.namespace, GenerateName: args[0] + "-check-"},
				Spec:       orbv1.OrbBenchmarkRunSpec{BenchmarkRef: args[0], CheckOnly: true},
			}
			if err := k.client.Create(cmd.Context(), run); err != nil {
				return fmt.Errorf("creating the check run: %w", err)
			}
			fmt.Fprintf(o.out, "Checking the registry of benchmark %s (run %s)...\n", args[0], run.Name)

			if !keep {
				defer func() {
					// Use a fresh context so the check run is removed even after a timeout.
					_ = k.client.Delete(context.Background(), run)
				}()
			}

			finished, err := waitForRun(cmd.Context(), o, k, run.Name, timeout)
			if err != nil {
				return err
			}
			return reportChecks(o, finished)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "how long to wait for the check to finish")
	cmd.Flags().BoolVar(&keep, "keep", false, "keep the check run afterwards instead of deleting it")
	return cmd
}

// waitForRun polls a run until it finishes or the timeout passes.
func waitForRun(ctx context.Context, o *options, k *kube, name string, timeout time.Duration) (*orbv1.OrbBenchmarkRun, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()

	for {
		var run orbv1.OrbBenchmarkRun
		if err := k.client.Get(ctx, client.ObjectKey{Namespace: k.namespace, Name: name}, &run); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("orbbenchmarkrun/%s did not finish within %s", name, timeout)
			}
			return nil, fmt.Errorf("reading orbbenchmarkrun/%s: %w", name, err)
		}
		if run.Status.Phase.IsFinished() {
			return &run, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("orbbenchmarkrun/%s did not finish within %s (phase %s)", name, timeout, orDash(string(run.Status.Phase)))
		case <-ticker.C:
		}
	}
}

// reportChecks prints the check results and returns an error if the check
// run failed or any check did not pass.
func reportChecks(o *options, run *orbv1.OrbBenchmarkRun) error {
	if len(run.Status.Checks) > 0 {
		if err := printChecks(o.out, run.Status.Checks); err != nil {
			return err
		}
	}
	if run.Status.RegistryVersion != "" {
		fmt.Fprintf(o.out, "Registry version: %s\n", run.Status.RegistryVersion)
	}

	failed := 0
	for _, c := range run.Status.Checks {
		if !c.Passed {
			failed++
		}
	}
	switch {
	case run.Status.Phase != orbv1.PhaseSucceeded:
		return fmt.Errorf("the registry check %s: %s", lowerPhase(run.Status.Phase), orDash(run.Status.Message))
	case failed > 0:
		return fmt.Errorf("%d registry check(s) failed", failed)
	default:
		fmt.Fprintln(o.out, "All registry checks passed.")
		return nil
	}
}

func lowerPhase(p orbv1.RunPhase) string {
	switch p {
	case orbv1.PhaseFailed:
		return "failed"
	case orbv1.PhaseAborted:
		return "was aborted"
	default:
		return "ended in phase " + string(p)
	}
}
