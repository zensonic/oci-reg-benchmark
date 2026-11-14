// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OrbBenchmarkResult holds the summary results of one run (RES-1). Detailed,
// full-resolution data is stored in the controller's SQLite database and can
// be exported with "orbctl result export" (section 11.3).
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=orbres,categories=orb
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.summary.runName`
// +kubebuilder:printcolumn:name="Benchmark",type=string,JSONPath=`.summary.benchmarkName`
// +kubebuilder:printcolumn:name="Aborted",type=boolean,JSONPath=`.summary.aborted`
// +kubebuilder:printcolumn:name="Saturated",type=boolean,JSONPath=`.summary.saturated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type OrbBenchmarkResult struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Summary ResultSummary `json:"summary"`
}

// OrbBenchmarkResultList is a list of OrbBenchmarkResult objects.
//
// +kubebuilder:object:root=true
type OrbBenchmarkResultList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OrbBenchmarkResult `json:"items"`
}

// ResultSummary is the compact summary of a run (section 11.1).
type ResultSummary struct {
	RunName       string `json:"runName"`
	RunUID        string `json:"runUID"`
	BenchmarkName string `json:"benchmarkName"`

	// Aborted is true if the run was aborted before it finished (RUN-6).
	// +optional
	Aborted bool `json:"aborted,omitempty"`

	StartedAt   metav1.Time `json:"startedAt"`
	CompletedAt metav1.Time `json:"completedAt"`

	// Registry is the registry that was tested (RES-6).
	Registry RegistryInfo `json:"registry"`

	// Agents is the number of agent pods.
	Agents int `json:"agents"`

	// Saturated is true if the agents themselves were the bottleneck (LOAD-9).
	// +optional
	Saturated bool `json:"saturated,omitempty"`

	// SaturationReason explains why the run was flagged as saturated.
	// +optional
	SaturationReason string `json:"saturationReason,omitempty"`

	// Detail points to the full-resolution data in SQLite (RES-7).
	Detail DetailReference `json:"detail"`

	// BenchmarkSpec is the spec snapshot the run used (RES-6).
	BenchmarkSpec OrbBenchmarkSpec `json:"benchmarkSpec"`

	// Images are the static images and their digests (RES-6).
	// +optional
	Images []ResolvedImage `json:"images,omitempty"`

	// Scenarios has one entry per scenario (TYPE-3).
	// +optional
	Scenarios []ScenarioResult `json:"scenarios,omitempty"`

	// Cleanup describes what cleanup did.
	// +optional
	Cleanup string `json:"cleanup,omitempty"`
}

// RegistryInfo describes the registry that was tested.
type RegistryInfo struct {
	URL    string `json:"url"`
	Module string `json:"module"`
	// +optional
	Version string `json:"version,omitempty"`
}

// DetailReference points to a run's detail data in SQLite.
type DetailReference struct {
	// Store is the kind of detail store. Always "sqlite" for now.
	Store string `json:"store"`
	// RunUID is the key of the run's rows in the store.
	RunUID string `json:"runUID"`
}

// ScenarioResult is the summary of one scenario.
type ScenarioResult struct {
	Name string       `json:"name"`
	Type ScenarioType `json:"type"`

	// DurationSeconds is the measured time, excluding warm-up.
	DurationSeconds float64 `json:"durationSeconds"`

	// Operations has the totals per operation type, excluding warm-up (RES-3).
	// +optional
	Operations []OperationResult `json:"operations,omitempty"`

	// Warmup has the totals per operation type during warm-up (LOAD-5).
	// +optional
	Warmup []OperationResult `json:"warmup,omitempty"`

	// Stages has the figures per stage (RES-4).
	// +optional
	Stages []StageResult `json:"stages,omitempty"`

	// TimeSeries is a coarse time series of the key figures (RES-4, RES-5).
	// +optional
	TimeSeries *TimeSeries `json:"timeSeries,omitempty"`
}

// OperationResult are the figures for one operation type (RES-3).
type OperationResult struct {
	Operation string `json:"operation"`
	Count     int64  `json:"count"`
	Errors    int64  `json:"errors"`

	// ErrorsByType counts errors per type, for example "http_503" or "timeout".
	// +optional
	ErrorsByType map[string]int64 `json:"errorsByType,omitempty"`

	OpsPerSecond float64 `json:"opsPerSecond"`

	// Latency as defined in section 8 for this operation.
	Latency LatencySummary `json:"latency"`

	// Total is the time for the whole operation, from start to finish.
	// +optional
	Total *LatencySummary `json:"total,omitempty"`

	// Throughput is set for operations that transfer blob data (section 9).
	// +optional
	Throughput *ThroughputSummary `json:"throughput,omitempty"`

	// Phases summarize where the time went (LAT-6), keyed by phase name.
	// +optional
	Phases map[string]LatencySummary `json:"phases,omitempty"`

	// Counters are extra counts, for example "pages" or "mountMisses" (OP-11, DEDUP-6).
	// +optional
	Counters map[string]int64 `json:"counters,omitempty"`
}

// LatencySummary are latency percentiles in milliseconds.
type LatencySummary struct {
	MinMillis  float64 `json:"minMillis"`
	P50Millis  float64 `json:"p50Millis"`
	P90Millis  float64 `json:"p90Millis"`
	P95Millis  float64 `json:"p95Millis"`
	P99Millis  float64 `json:"p99Millis"`
	P999Millis float64 `json:"p999Millis"`
	MaxMillis  float64 `json:"maxMillis"`
	MeanMillis float64 `json:"meanMillis"`
}

// ThroughputSummary describes data transfer for push or pull (section 9).
type ThroughputSummary struct {
	// TotalBytes transferred.
	TotalBytes int64 `json:"totalBytes"`
	// AggregateBytesPerSecond is total bytes divided by measured time (THR-2).
	AggregateBytesPerSecond float64 `json:"aggregateBytesPerSecond"`
	// PerOperation are percentiles of per-operation throughput in bytes per
	// second, measured from first to last byte (THR-1).
	PerOperation ThroughputPercentiles `json:"perOperation"`
}

// ThroughputPercentiles are per-operation throughput percentiles in bytes per second.
type ThroughputPercentiles struct {
	P1  float64 `json:"p1"`
	P50 float64 `json:"p50"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// StageResult are the figures for one stage of a scenario (RES-4).
type StageResult struct {
	Index   int `json:"index"`
	Workers int `json:"workers"`
	// +optional
	Operations []StageOperation `json:"operations,omitempty"`
}

// StageOperation are brief figures for one operation type within a stage.
type StageOperation struct {
	Operation    string  `json:"operation"`
	Count        int64   `json:"count"`
	Errors       int64   `json:"errors"`
	OpsPerSecond float64 `json:"opsPerSecond"`
	P50Millis    float64 `json:"p50Millis"`
	P99Millis    float64 `json:"p99Millis"`
}

// TimeSeries is a coarse time series for one scenario (RES-4, RES-5).
type TimeSeries struct {
	// BucketSeconds is the width of each point.
	BucketSeconds int `json:"bucketSeconds"`
	// +optional
	Points []TimePoint `json:"points,omitempty"`
}

// TimePoint are the figures for one bucket of the time series.
type TimePoint struct {
	// OffsetSeconds from the start of the scenario.
	OffsetSeconds int `json:"offsetSeconds"`
	Workers       int `json:"workers"`
	// +optional
	Operations []TimePointOperation `json:"operations,omitempty"`
}

// TimePointOperation are the figures for one operation type in one bucket.
type TimePointOperation struct {
	Operation      string  `json:"operation"`
	OpsPerSecond   float64 `json:"opsPerSecond"`
	P50Millis      float64 `json:"p50Millis"`
	P99Millis      float64 `json:"p99Millis"`
	BytesPerSecond float64 `json:"bytesPerSecond"`
	Errors         int64   `json:"errors"`
}
