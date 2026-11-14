// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package protocol defines the messages exchanged between the ORB agents and
// the controller, and the detail data served by the export endpoint.
//
// Agents talk to the controller over plain HTTP with JSON bodies. Every
// request carries the run's agent token as a bearer token. The agent asks
// the controller what to do next (Command), tells it what it finished
// (StateUpdate) and sends metrics once per measurement interval
// (IntervalReport, ARCH-6).
package protocol

import (
	"fmt"
	"strings"
	"time"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

// HTTP paths of the agent API. {uid} is the run UID and {agent} the agent index.
const (
	PathPlan    = "/agent/v1/runs/{uid}/plan"
	PathCommand = "/agent/v1/runs/{uid}/agents/{agent}/command"
	PathState   = "/agent/v1/runs/{uid}/agents/{agent}/state"
	PathReport  = "/agent/v1/runs/{uid}/agents/{agent}/report"

	// PathExport serves the detail data of a run (RES-13).
	// Add "?format=sqlite" to download a standalone SQLite file instead of JSON.
	PathExport = "/export/v1/runs/{uid}"
)

// AgentPath fills in the run UID and agent index in one of the agent API paths.
func AgentPath(pattern, runUID string, agent int) string {
	return strings.NewReplacer("{uid}", runUID, "{agent}", fmt.Sprint(agent)).Replace(pattern)
}

// ExportPath returns the export path for a run.
func ExportPath(runUID string) string {
	return strings.ReplaceAll(PathExport, "{uid}", runUID)
}

// Plan is everything an agent needs to know about the run it belongs to.
type Plan struct {
	RunName       string `json:"runName"`
	RunUID        string `json:"runUID"`
	Namespace     string `json:"namespace"`
	BenchmarkName string `json:"benchmarkName"`

	// Spec is the defaulted benchmark spec snapshot with overrides applied.
	Spec orbv1.OrbBenchmarkSpec `json:"spec"`

	// Agents is the total number of agents in the run.
	Agents int `json:"agents"`

	// RepositoryRoot is where this run pushes: <repositoryPrefix>/<run name> (IMG-12).
	RepositoryRoot string `json:"repositoryRoot"`

	// CheckOnly runs only the registry checks, without load.
	CheckOnly bool `json:"checkOnly"`
}

// Action tells an agent what to do next.
type Action string

// Actions an agent can be told to perform.
const (
	// ActionWait means keep polling; nothing to do yet.
	ActionWait Action = "wait"
	// ActionSeed means push the seed content this agent is responsible for.
	ActionSeed Action = "seed"
	// ActionRun means run the scenario with index Command.Scenario, starting
	// at Command.StartAt (LOAD-3).
	ActionRun Action = "run"
	// ActionCleanup means delete what the run pushed (only sent to agent 0).
	ActionCleanup Action = "cleanup"
	// ActionAbort means stop all workers now, send final reports and exit.
	ActionAbort Action = "abort"
	// ActionExit means the run is over; exit successfully.
	ActionExit Action = "exit"
)

// Command is the controller's answer to an agent asking what to do.
type Command struct {
	Action Action `json:"action"`
	// Scenario is the scenario index for ActionRun.
	Scenario int `json:"scenario,omitempty"`
	// StartAt is the shared start time for ActionRun (LOAD-3).
	StartAt time.Time `json:"startAt,omitzero"`
}

// AgentState is what an agent reports when it finishes a step.
type AgentState string

// States reported by agents.
const (
	StateReady        AgentState = "ready"        // preparing is done (and checks passed, for agent 0)
	StateSeeded       AgentState = "seeded"       // seeding is done
	StateScenarioDone AgentState = "scenarioDone" // StateUpdate.Scenario has finished
	StateCleanedUp    AgentState = "cleanedUp"    // cleanup is done (agent 0)
	StateAborted      AgentState = "aborted"      // stopped after an abort
	StateFailed       AgentState = "failed"       // a fatal error; Message says why
)

// StateUpdate is sent by an agent when it finishes a step or fails.
type StateUpdate struct {
	State AgentState `json:"state"`

	// Scenario is the scenario index for StateScenarioDone.
	Scenario int `json:"scenario,omitempty"`

	// Message explains a failure, or describes the result of a step.
	Message string `json:"message,omitempty"`

	// The fields below are only set by agent 0 with StateReady.

	// Checks are the registry check results (REG-7).
	Checks []orbv1.CheckResult `json:"checks,omitempty"`
	// ResolvedImages are the static images and their digests (IMG-8).
	ResolvedImages []orbv1.ResolvedImage `json:"resolvedImages,omitempty"`
	// RegistryVersion is the registry version, if known.
	RegistryVersion string `json:"registryVersion,omitempty"`
}

// Metric names used as keys in OperationReport.Histograms.
const (
	// MetricLatency is the latency as defined in section 8 for the operation.
	MetricLatency = "latency"
	// MetricTotal is the time for the whole operation.
	MetricTotal = "total"
	// MetricThroughput is per-operation throughput in bytes per second (THR-1).
	MetricThroughput = "throughput"
	// MetricBlobStep is the time of each per-blob deduplication step (DEDUP-5).
	MetricBlobStep = "blobStep"
	// MetricPhasePrefix is prepended to phase names, for example "phase:dns" (LAT-6).
	MetricPhasePrefix = "phase:"
)

// IntervalReport carries one agent's metrics for one measurement interval.
type IntervalReport struct {
	// Scenario is the scenario index; ScenarioName its name.
	Scenario     int    `json:"scenario"`
	ScenarioName string `json:"scenarioName"`

	// Interval is the interval number since the scenario's shared start time.
	// Interval 0 covers the first measurement interval.
	Interval int `json:"interval"`

	// Stage is the load profile stage the interval belongs to.
	Stage int `json:"stage"`

	// Warmup is true if the interval is part of the warm-up period (LOAD-5).
	Warmup bool `json:"warmup"`

	// Workers is the number of workers active at the end of the interval.
	Workers int `json:"workers"`

	// CPUUtilization is the agent's CPU use, from 0 to 1 (LOAD-9).
	CPUUtilization float64 `json:"cpuUtilization"`

	// SchedDelayP99Millis is the 99th percentile Go scheduler delay (LOAD-9).
	SchedDelayP99Millis float64 `json:"schedDelayP99Millis"`

	// Operations has one entry per operation type seen in the interval.
	Operations []OperationReport `json:"operations,omitempty"`

	// Samples are raw per-operation samples, only sent if enabled (RES-9).
	Samples []Sample `json:"samples,omitempty"`
}

// OperationReport are the metrics for one operation type in one interval.
type OperationReport struct {
	Operation string `json:"operation"`

	// Count is the number of operations that finished in the interval,
	// including failed ones.
	Count int64 `json:"count"`

	// Bytes is the blob data transferred by operations in the interval.
	Bytes int64 `json:"bytes"`

	// Errors counts failed operations by error type (RES-3).
	Errors map[string]int64 `json:"errors,omitempty"`

	// ErrorExamples holds one example message per error type.
	ErrorExamples map[string]string `json:"errorExamples,omitempty"`

	// Counters are extra counts, for example "pages" or "mountMisses".
	Counters map[string]int64 `json:"counters,omitempty"`

	// Histograms are HDR histograms, keyed by metric name, in the compressed
	// V2 encoding. Latencies are in microseconds; throughput in bytes/second.
	Histograms map[string][]byte `json:"histograms,omitempty"`
}

// Sample is one raw operation (RES-9).
type Sample struct {
	Operation string `json:"operation"`
	// OffsetMicros is when the operation started, relative to the scenario start.
	OffsetMicros  int64  `json:"offsetMicros"`
	LatencyMicros int64  `json:"latencyMicros"`
	TotalMicros   int64  `json:"totalMicros"`
	Bytes         int64  `json:"bytes"`
	Error         string `json:"error,omitempty"`
}

// Detail is the full-resolution detail data of a run, served by the export
// endpoint as JSON (RES-13, RES-14).
type Detail struct {
	RunUID    string           `json:"runUID"`
	Scenarios []ScenarioDetail `json:"scenarios"`
}

// ScenarioDetail is the detail data of one scenario.
type ScenarioDetail struct {
	Name string `json:"name"`

	// Intervals has per-interval figures, merged across all agents.
	Intervals []IntervalDetail `json:"intervals"`

	// Phases summarize each phase of each operation over the whole scenario,
	// excluding warm-up (LAT-6).
	Phases []PhaseDetail `json:"phases"`

	// Agents has each agent's own figures per interval (LOAD-9).
	Agents []AgentInterval `json:"agents"`

	// Errors lists error counts with an example message.
	Errors []ErrorDetail `json:"errors"`

	// Samples are the raw samples, if they were recorded (RES-9).
	Samples []AgentSample `json:"samples,omitempty"`
}

// IntervalDetail are the figures for one interval, merged across agents.
type IntervalDetail struct {
	Interval   int                 `json:"interval"`
	Stage      int                 `json:"stage"`
	Warmup     bool                `json:"warmup"`
	Workers    int                 `json:"workers"`
	Operations []IntervalOperation `json:"operations"`
}

// IntervalOperation are the figures for one operation type in one interval.
type IntervalOperation struct {
	Operation string  `json:"operation"`
	Count     int64   `json:"count"`
	Errors    int64   `json:"errors"`
	Bytes     int64   `json:"bytes"`
	P50Millis float64 `json:"p50Millis"`
	P90Millis float64 `json:"p90Millis"`
	P99Millis float64 `json:"p99Millis"`
	MaxMillis float64 `json:"maxMillis"`
}

// PhaseDetail summarizes one phase (or metric) of one operation type.
type PhaseDetail struct {
	Operation string               `json:"operation"`
	Metric    string               `json:"metric"`
	Count     int64                `json:"count"`
	Latency   orbv1.LatencySummary `json:"latency"`
}

// AgentInterval are one agent's own figures for one interval.
type AgentInterval struct {
	Agent               int     `json:"agent"`
	Interval            int     `json:"interval"`
	Workers             int     `json:"workers"`
	CPUUtilization      float64 `json:"cpuUtilization"`
	SchedDelayP99Millis float64 `json:"schedDelayP99Millis"`
}

// ErrorDetail is the count of one error type for one operation type.
type ErrorDetail struct {
	Operation string `json:"operation"`
	Type      string `json:"type"`
	Count     int64  `json:"count"`
	Example   string `json:"example,omitempty"`
}

// AgentSample is a raw sample together with the agent that recorded it.
type AgentSample struct {
	Agent int `json:"agent"`
	Sample
}
