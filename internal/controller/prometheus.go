// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// Live Prometheus metrics, exposed on the controller's metrics endpoint
// while runs are in progress (RES-15).
var (
	operationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "orb_operations_total",
		Help: "Registry operations finished by ORB agents, including failed ones.",
	}, []string{"namespace", "run", "scenario", "operation"})

	operationErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "orb_operation_errors_total",
		Help: "Failed registry operations, by error type.",
	}, []string{"namespace", "run", "scenario", "operation", "type"})

	bytesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "orb_bytes_total",
		Help: "Blob bytes transferred by ORB agents.",
	}, []string{"namespace", "run", "scenario", "operation"})

	activeWorkers = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "orb_active_workers",
		Help: "Workers active across all agents in the latest interval.",
	}, []string{"namespace", "run"})

	liveP99Seconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "orb_live_latency_p99_seconds",
		Help: "99th percentile latency in the latest complete interval.",
	}, []string{"namespace", "run", "operation"})

	registerOnce sync.Once
)

// registerPrometheus adds ORB's metrics to controller-runtime's registry.
func registerPrometheus() {
	registerOnce.Do(func() {
		crmetrics.Registry.MustRegister(operationsTotal, operationErrorsTotal, bytesTotal, activeWorkers, liveP99Seconds)
	})
}

func recordPrometheus(namespace, run, scenario string, op protocol.OperationReport) {
	operationsTotal.WithLabelValues(namespace, run, scenario, op.Operation).Add(float64(op.Count))
	bytesTotal.WithLabelValues(namespace, run, scenario, op.Operation).Add(float64(op.Bytes))
	for errorType, n := range op.Errors {
		operationErrorsTotal.WithLabelValues(namespace, run, scenario, op.Operation, errorType).Add(float64(n))
	}
}

func setActiveWorkers(namespace, run string, workers int) {
	activeWorkers.WithLabelValues(namespace, run).Set(float64(workers))
}

func setLiveP99(namespace, run, operation string, seconds float64) {
	liveP99Seconds.WithLabelValues(namespace, run, operation).Set(seconds)
}

// forgetRunMetrics removes a run's gauges once it has finished.
func forgetRunMetrics(namespace, run string) {
	labels := prometheus.Labels{"namespace": namespace, "run": run}
	activeWorkers.DeletePartialMatch(labels)
	liveP99Seconds.DeletePartialMatch(labels)
}
