// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package controller is the ORB operator (ARCH-4). It reconciles
// OrbBenchmarkRun objects, coordinates the agents of each run over the
// agent API, merges their metrics, stores detail data in SQLite and writes
// OrbBenchmarkResult objects.
package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

// Options configure the controller.
type Options struct {
	// MetricsAddr is where Prometheus metrics are served (default ":8080").
	MetricsAddr string
	// HealthAddr serves /healthz and /readyz (default ":8081").
	HealthAddr string
	// AgentAPIAddr serves the agent API and the export endpoint (default ":8082").
	AgentAPIAddr string
	// DBPath is the SQLite database file (default "/data/orb.db").
	DBPath string
	// AgentImage is the image used for agent pods (normally the controller's own image).
	AgentImage string
	// AgentAPIURL is how agents reach the agent API, for example
	// http://orb-controller.orb-system.svc:8082.
	AgentAPIURL string
	// LeaderElection makes sure only one controller is active (ARCH-8).
	LeaderElection bool
	// LeaderElectionNamespace is where the leader election lease lives.
	LeaderElectionNamespace string
}

func (o *Options) setDefaults() {
	if o.MetricsAddr == "" {
		o.MetricsAddr = ":8080"
	}
	if o.HealthAddr == "" {
		o.HealthAddr = ":8081"
	}
	if o.AgentAPIAddr == "" {
		o.AgentAPIAddr = ":8082"
	}
	if o.DBPath == "" {
		o.DBPath = "/data/orb.db"
	}
}

// Run starts the controller and blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	opts.setDefaults()
	if opts.AgentImage == "" || opts.AgentAPIURL == "" {
		return errors.New("the agent image and the agent API URL must be set")
	}
	logger := ctrl.Log.WithName("orb-controller")

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := orbv1.AddToScheme(scheme); err != nil {
		return err
	}

	st, err := store.Open(opts.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	config, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("loading Kubernetes configuration: %w", err)
	}
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: opts.MetricsAddr},
		HealthProbeBindAddress:  opts.HealthAddr,
		LeaderElection:          opts.LeaderElection,
		LeaderElectionID:        "orb-controller.orb.kubectl.dk",
		LeaderElectionNamespace: opts.LeaderElectionNamespace,
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}
	registerPrometheus()

	coordinator := NewCoordinator()
	reconciler := &RunReconciler{
		Client:      mgr.GetClient(),
		Scheme:      scheme,
		Coordinator: coordinator,
		Store:       st,
		Jobs:        jobSettings{Image: opts.AgentImage, AgentAPIURL: opts.AgentAPIURL},
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return err
	}

	// The agent API runs only on the leader, together with the reconciler,
	// because both use the same in-memory coordinator.
	server := &http.Server{
		Addr:              opts.AgentAPIAddr,
		Handler:           newAPIHandler(coordinator, st, logger.WithName("agent-api")),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		return serveUntilDone(ctx, server)
	})); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	logger.Info("starting", "agentAPI", opts.AgentAPIAddr, "database", opts.DBPath, "agentImage", opts.AgentImage)
	return mgr.Start(ctx)
}

// serveUntilDone runs an HTTP server until ctx is cancelled.
func serveUntilDone(ctx context.Context, server *http.Server) error {
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	select {
	case err := <-errs:
		return fmt.Errorf("agent API server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
