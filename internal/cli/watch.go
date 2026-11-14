// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

func newRunWatchCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "watch RUN",
		Short: "Follow a run until it finishes",
		Long: "Follow a run and print its phase and live figures every few seconds.\n" +
			"Exits with a non-zero status if the run fails or is aborted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := o.client()
			if err != nil {
				return err
			}
			return watchRun(cmd, o, k, args[0])
		},
	}
}

// watchRun polls a run until it reaches a final phase. It prints a line on
// every phase change and the live table while the run is measuring.
func watchRun(cmd *cobra.Command, o *options, k *kube, name string) error {
	var lastPhase orbv1.RunPhase
	var lastScenario string
	var lastLive time.Time

	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()

	for {
		run, err := getRun(cmd, k, name)
		if err != nil {
			return err
		}
		s := run.Status

		if s.Phase != lastPhase || s.ActiveScenario != lastScenario {
			line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), orDash(string(s.Phase)))
			if s.ActiveScenario != "" {
				line += ", scenario " + s.ActiveScenario
			}
			if s.Phase == orbv1.PhasePreparing {
				line += fmt.Sprintf(", %d of %d agents ready", s.AgentsReady, s.Agents)
			}
			if s.Message != "" {
				line += ": " + s.Message
			}
			fmt.Fprintln(o.out, line)
			lastPhase, lastScenario = s.Phase, s.ActiveScenario
		}

		if s.Live != nil && s.Phase == orbv1.PhaseRunning && s.Live.UpdatedAt.After(lastLive) {
			lastLive = s.Live.UpdatedAt.Time
			if err := printLive(o.out, s.Live); err != nil {
				return err
			}
		}

		if s.Phase.IsFinished() {
			return finishedRunResult(o, run)
		}

		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-ticker.C:
		}
	}
}

// finishedRunResult prints where to find the results and turns a failed or
// aborted run into an error, so scripts see a non-zero exit status.
func finishedRunResult(o *options, run *orbv1.OrbBenchmarkRun) error {
	if run.Status.ResultName != "" {
		fmt.Fprintf(o.out, "Results: orbctl result get %s\n", run.Name)
	}
	switch run.Status.Phase {
	case orbv1.PhaseFailed:
		return fmt.Errorf("orbbenchmarkrun/%s failed: %s", run.Name, orDash(run.Status.Message))
	case orbv1.PhaseAborted:
		return fmt.Errorf("orbbenchmarkrun/%s was aborted", run.Name)
	default:
		return nil
	}
}
