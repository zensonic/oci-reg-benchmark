// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OrbBenchmarkRun is one execution of an OrbBenchmark (RUN-2).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=orbr,categories=orb
// +kubebuilder:printcolumn:name="Benchmark",type=string,JSONPath=`.spec.benchmarkRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Scenario",type=string,JSONPath=`.status.activeScenario`
// +kubebuilder:printcolumn:name="Agents",type=integer,JSONPath=`.status.agents`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type OrbBenchmarkRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OrbBenchmarkRunSpec   `json:"spec"`
	Status OrbBenchmarkRunStatus `json:"status,omitempty"`
}

// OrbBenchmarkRunList is a list of OrbBenchmarkRun objects.
//
// +kubebuilder:object:root=true
type OrbBenchmarkRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OrbBenchmarkRun `json:"items"`
}

// OrbBenchmarkRunSpec says which benchmark to run and what to override.
type OrbBenchmarkRunSpec struct {
	// BenchmarkRef is the name of the OrbBenchmark in the same namespace.
	// +kubebuilder:validation:MinLength=1
	BenchmarkRef string `json:"benchmarkRef"`

	// Overrides change selected benchmark fields for this run only (RUN-4).
	// +optional
	Overrides *RunOverrides `json:"overrides,omitempty"`

	// Abort stops a running run. Results gathered so far are kept (RUN-6).
	// +optional
	Abort bool `json:"abort,omitempty"`

	// CheckOnly only checks connectivity, authentication and supported
	// operations, without generating load. Used by "orbctl registry check".
	// +optional
	CheckOnly bool `json:"checkOnly,omitempty"`
}

// RunOverrides change selected benchmark fields for one run (RUN-4).
type RunOverrides struct {
	// Cleanup overrides the cleanup policy.
	// +optional
	Cleanup *CleanupPolicy `json:"cleanup,omitempty"`

	// Seed overrides the random seed.
	// +optional
	Seed *int64 `json:"seed,omitempty"`

	// RepositoryPrefix overrides the registry repository prefix.
	// +optional
	RepositoryPrefix *string `json:"repositoryPrefix,omitempty"`

	// WorkersPerAgent overrides the agent worker limit.
	// +optional
	WorkersPerAgent *int `json:"workersPerAgent,omitempty"`

	// Stages overrides the load profile stages of the named scenarios.
	// +optional
	Stages map[string][]Stage `json:"stages,omitempty"`
}

// RunPhase is the lifecycle phase of a run (RUN-5).
// +kubebuilder:validation:Enum=Pending;Preparing;Seeding;Running;Collecting;CleaningUp;Succeeded;Failed;Aborted
type RunPhase string

// Run phases, in the order a run moves through them (RUN-5).
const (
	PhasePending    RunPhase = "Pending"
	PhasePreparing  RunPhase = "Preparing"
	PhaseSeeding    RunPhase = "Seeding"
	PhaseRunning    RunPhase = "Running"
	PhaseCollecting RunPhase = "Collecting"
	PhaseCleaningUp RunPhase = "CleaningUp"
	PhaseSucceeded  RunPhase = "Succeeded"
	PhaseFailed     RunPhase = "Failed"
	PhaseAborted    RunPhase = "Aborted"
)

// IsFinished reports whether the phase is a final phase.
func (p RunPhase) IsFinished() bool {
	return p == PhaseSucceeded || p == PhaseFailed || p == PhaseAborted
}

// OrbBenchmarkRunStatus is the observed state of a run.
type OrbBenchmarkRunStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`

	// Message explains the phase, for example why a run failed.
	// +optional
	Message string `json:"message,omitempty"`

	// ActiveScenario is the scenario that is running now.
	// +optional
	ActiveScenario string `json:"activeScenario,omitempty"`

	// Agents is the number of agent pods.
	// +optional
	Agents int `json:"agents,omitempty"`

	// AgentsReady is the number of agents that finished preparing.
	// +optional
	AgentsReady int `json:"agentsReady,omitempty"`

	// StartedAt is when the run left the Pending phase.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the run reached a final phase.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// BenchmarkSpec is the complete benchmark spec used by this run, with
	// overrides applied. Later edits to the OrbBenchmark do not change it (RUN-3).
	// +optional
	BenchmarkSpec *OrbBenchmarkSpec `json:"benchmarkSpec,omitempty"`

	// ResolvedImages are the static images with the digests they resolved to (IMG-8).
	// +optional
	ResolvedImages []ResolvedImage `json:"resolvedImages,omitempty"`

	// RegistryVersion is the registry version, if the registry reports it.
	// +optional
	RegistryVersion string `json:"registryVersion,omitempty"`

	// Checks are the results of the registry checks done while preparing (REG-7).
	// +optional
	Checks []CheckResult `json:"checks,omitempty"`

	// Live shows figures from the most recent measurement interval.
	// +optional
	Live *LiveStats `json:"live,omitempty"`

	// ResultName is the OrbBenchmarkResult that holds this run's results.
	// +optional
	ResultName string `json:"resultName,omitempty"`

	// Cleanup describes what cleanup did, for example "deleted 4 repositories".
	// +optional
	Cleanup string `json:"cleanup,omitempty"`
}

// ResolvedImage is a static image reference and the digest it resolved to.
type ResolvedImage struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	// Size is the total size of the image's blobs in bytes.
	Size int64 `json:"size"`
}

// CheckResult is the outcome of one registry check.
type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

// LiveStats are figures from the most recent measurement interval.
type LiveStats struct {
	// Scenario the figures belong to.
	Scenario string `json:"scenario"`
	// Workers active across all agents.
	Workers int `json:"workers"`
	// Operations has one entry per operation type seen in the interval.
	// +optional
	Operations []LiveOperation `json:"operations,omitempty"`
	// UpdatedAt is when the figures were computed.
	UpdatedAt metav1.Time `json:"updatedAt"`
}

// LiveOperation are the live figures for one operation type.
type LiveOperation struct {
	Operation      string  `json:"operation"`
	OpsPerSecond   float64 `json:"opsPerSecond"`
	P50Millis      float64 `json:"p50Millis"`
	P99Millis      float64 `json:"p99Millis"`
	BytesPerSecond float64 `json:"bytesPerSecond"`
	Errors         int64   `json:"errors"`
}
