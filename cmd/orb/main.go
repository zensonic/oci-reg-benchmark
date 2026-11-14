// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Command orb is the single binary for the ORB controller and agent (ENG-5).
//
//	orb controller   runs the operator (normally started by the Helm chart)
//	orb agent        runs one agent pod (started by the controller)
//	orb version      prints the build version
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zensonic/oci-reg-benchmark/internal/agent"
	"github.com/zensonic/oci-reg-benchmark/internal/controller"
	"github.com/zensonic/oci-reg-benchmark/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run executes the command; it returns instead of exiting so deferred
// cleanup always runs.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newRootCommand().ExecuteContext(ctx)
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "orb",
		Short:         "ORB controller and agent",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newControllerCommand(), newAgentCommand(), newVersionCommand())
	return root
}

func newControllerCommand() *cobra.Command {
	opts := controller.Options{}
	cmd := &cobra.Command{
		Use:   "controller",
		Short: "Run the ORB operator",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return controller.Run(cmd.Context(), opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.MetricsAddr, "metrics-addr", ":8080", "address of the Prometheus metrics endpoint")
	flags.StringVar(&opts.HealthAddr, "health-addr", ":8081", "address of the health probes")
	flags.StringVar(&opts.AgentAPIAddr, "agent-api-addr", ":8082", "address of the agent API and export endpoint")
	flags.StringVar(&opts.DBPath, "db-path", "/data/orb.db", "path of the SQLite database")
	flags.StringVar(&opts.AgentImage, "agent-image", os.Getenv("ORB_AGENT_IMAGE"), "image for agent pods")
	flags.StringVar(&opts.AgentAPIURL, "agent-api-url", os.Getenv("ORB_AGENT_API_URL"), "URL agents use to reach the agent API")
	flags.BoolVar(&opts.LeaderElection, "leader-elect", true, "use leader election so only one controller is active")
	flags.StringVar(&opts.LeaderElectionNamespace, "leader-election-namespace", os.Getenv("POD_NAMESPACE"), "namespace of the leader election lease")
	return cmd
}

func newAgentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "agent",
		Short: "Run one ORB agent (started by the controller)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return agent.Run(cmd.Context())
		},
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "orb", version.String())
		},
	}
}
