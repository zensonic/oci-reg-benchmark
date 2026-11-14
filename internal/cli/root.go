// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package cli implements orbctl, the command-line tool for ORB (section 14
// of requirements.md). orbctl talks to the Kubernetes API with the user's
// kubeconfig, so access is controlled by Kubernetes RBAC (CLI-1). Everything
// it does with benchmarks, runs and results can also be done with kubectl
// and plain YAML (CLI-3).
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zensonic/oci-reg-benchmark/internal/version"
)

// options are the global flags and shared state of all commands.
type options struct {
	kubeconfig          string
	kubeContext         string
	namespace           string
	controllerNamespace string

	out    io.Writer
	errOut io.Writer

	// pollInterval is how often watch and wait loops check a run.
	pollInterval time.Duration

	// connect creates the Kubernetes connection. Tests replace it with a
	// fake client.
	connect func(o *options) (*kube, error)
	kube    *kube
}

// client returns the Kubernetes connection, creating it on first use.
func (o *options) client() (*kube, error) {
	if o.kube != nil {
		return o.kube, nil
	}
	k, err := o.connect(o)
	if err != nil {
		return nil, err
	}
	o.kube = k
	return k, nil
}

// Execute runs orbctl with the process arguments and returns the exit code.
func Execute() int {
	cmd := newRootCommand(&options{
		out:          os.Stdout,
		errOut:       os.Stderr,
		pollInterval: 2 * time.Second,
		connect:      connectToCluster,
	})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	return 0
}

func newRootCommand(o *options) *cobra.Command {
	root := &cobra.Command{
		Use:   "orbctl",
		Short: "orbctl controls ORB, the OCI registry benchmark",
		Long: "orbctl creates and inspects ORB benchmarks, runs and results in Kubernetes.\n" +
			"It uses your kubeconfig, so your Kubernetes permissions decide what you may do.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(o.out)
	root.SetErr(o.errOut)

	flags := root.PersistentFlags()
	flags.StringVar(&o.kubeconfig, "kubeconfig", "", "path to the kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")
	flags.StringVar(&o.kubeContext, "context", "", "kubeconfig context to use")
	flags.StringVarP(&o.namespace, "namespace", "n", "", "namespace of the benchmarks and runs (default: from the kubeconfig context)")
	flags.StringVar(&o.controllerNamespace, "controller-namespace", "orb-system", "namespace where the ORB controller runs")

	root.AddCommand(
		newBenchmarkCommand(o),
		newRunCommand(o),
		newResultCommand(o),
		newRegistryCommand(o),
		newVersionCommand(o),
	)
	return root
}

func newVersionCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the orbctl version",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintln(o.out, "orbctl", version.String())
		},
	}
}
