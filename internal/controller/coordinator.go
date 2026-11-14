// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"fmt"
	"strings"
	"sync"
	"time"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// abortGracePeriod is how long the controller waits for agents to confirm an
// abort before it collects results anyway.
const abortGracePeriod = 30 * time.Second

// defaultStartDelay is how far in the future the shared scenario start time
// is set, so every agent receives it before it arrives (LOAD-3).
const defaultStartDelay = 3 * time.Second

// Coordinator holds the in-memory coordination state of every active run,
// keyed by run UID. It is shared by the reconciler and the agent API.
type Coordinator struct {
	mu   sync.Mutex
	runs map[string]*RunState
}

// NewCoordinator returns an empty coordinator.
func NewCoordinator() *Coordinator {
	return &Coordinator{runs: map[string]*RunState{}}
}

// Start registers a run, or returns the existing state if the run is
// already registered.
func (c *Coordinator) Start(plan protocol.Plan, token string, aggregator *Aggregator) *RunState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rs, ok := c.runs[plan.RunUID]; ok {
		return rs
	}
	rs := newRunState(plan, token, aggregator)
	c.runs[plan.RunUID] = rs
	return rs
}

// Get returns the state of a run.
func (c *Coordinator) Get(runUID string) (*RunState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rs, ok := c.runs[runUID]
	return rs, ok
}

// Remove forgets a run.
func (c *Coordinator) Remove(runUID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.runs, runUID)
}

// agentProgress is what one agent has finished.
type agentProgress struct {
	ready        bool
	seeded       bool
	doneScenario int // highest finished scenario index, -1 if none
	cleanedUp    bool
	aborted      bool
}

// RunState is the coordination state machine of one run. It decides which
// command each agent gets (see the command table in docs/architecture.md).
// It is safe for concurrent use.
type RunState struct {
	// Plan is sent to every agent. Token authenticates the agents.
	Plan  protocol.Plan
	Token string

	mu         sync.Mutex
	aggregator *Aggregator
	phase      orbv1.RunPhase
	scenario   int
	startAt    time.Time
	agents     []agentProgress

	abortRequested bool
	abortedAt      time.Time
	collected      bool
	message        string

	checks          []orbv1.CheckResult
	resolvedImages  []orbv1.ResolvedImage
	registryVersion string
	cleanup         string

	// now and startDelay can be replaced in tests.
	now        func() time.Time
	startDelay time.Duration
}

func newRunState(plan protocol.Plan, token string, aggregator *Aggregator) *RunState {
	agents := make([]agentProgress, plan.Agents)
	for i := range agents {
		agents[i].doneScenario = -1
	}
	return &RunState{
		Plan:       plan,
		Token:      token,
		aggregator: aggregator,
		phase:      orbv1.PhasePreparing,
		agents:     agents,
		now:        time.Now,
		startDelay: defaultStartDelay,
	}
}

// Aggregator returns the run's aggregator, or nil once results are written.
func (rs *RunState) Aggregator() *Aggregator {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.aggregator
}

// Command returns what an agent should do next.
func (rs *RunState) Command(agent int) protocol.Command {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.checkAbortTimeoutLocked()

	progress := rs.agents[agent]
	if rs.abortRequested && !rs.phase.IsFinished() {
		if progress.aborted {
			return protocol.Command{Action: protocol.ActionWait}
		}
		return protocol.Command{Action: protocol.ActionAbort}
	}

	switch rs.phase {
	case orbv1.PhaseSeeding:
		if !progress.seeded {
			return protocol.Command{Action: protocol.ActionSeed}
		}
	case orbv1.PhaseRunning:
		if progress.doneScenario < rs.scenario {
			return protocol.Command{Action: protocol.ActionRun, Scenario: rs.scenario, StartAt: rs.startAt}
		}
	case orbv1.PhaseCleaningUp:
		if agent != 0 {
			return protocol.Command{Action: protocol.ActionExit}
		}
		if !progress.cleanedUp {
			return protocol.Command{Action: protocol.ActionCleanup}
		}
	case orbv1.PhaseFailed:
		// Stop any workers that may still be running.
		return protocol.Command{Action: protocol.ActionAbort}
	case orbv1.PhaseSucceeded, orbv1.PhaseAborted:
		return protocol.Command{Action: protocol.ActionExit}
	}
	return protocol.Command{Action: protocol.ActionWait}
}

// Update records a state update from an agent and moves the run forward.
func (rs *RunState) Update(agent int, u protocol.StateUpdate) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if agent < 0 || agent >= len(rs.agents) {
		return fmt.Errorf("agent index %d is out of range", agent)
	}
	progress := &rs.agents[agent]

	switch u.State {
	case protocol.StateReady:
		progress.ready = true
		if agent == 0 {
			rs.checks = u.Checks
			rs.resolvedImages = u.ResolvedImages
			rs.registryVersion = u.RegistryVersion
		}
		rs.afterReadyLocked()
	case protocol.StateSeeded:
		progress.seeded = true
		if rs.phase == orbv1.PhaseSeeding && rs.allLocked(func(a agentProgress) bool { return a.seeded }) {
			rs.startScenarioLocked(0)
		}
	case protocol.StateScenarioDone:
		progress.doneScenario = max(progress.doneScenario, u.Scenario)
		rs.afterScenarioDoneLocked()
	case protocol.StateCleanedUp:
		progress.cleanedUp = true
		rs.cleanup = u.Message
		if rs.phase == orbv1.PhaseCleaningUp {
			rs.phase = orbv1.PhaseSucceeded
		}
	case protocol.StateAborted:
		progress.aborted = true
		if rs.abortRequested && rs.allLocked(func(a agentProgress) bool { return a.aborted }) {
			rs.phase = orbv1.PhaseCollecting
		}
	case protocol.StateFailed:
		// Agent 0 reports failed registry checks as a failure with the
		// check results attached; keep them for the run's status (REG-7).
		if len(u.Checks) > 0 {
			rs.checks = u.Checks
		}
		if u.RegistryVersion != "" {
			rs.registryVersion = u.RegistryVersion
		}
		rs.failLocked(fmt.Sprintf("agent %d failed: %s", agent, u.Message))
	default:
		return fmt.Errorf("unknown agent state %q", u.State)
	}
	return nil
}

// afterReadyLocked moves on once every agent is ready.
func (rs *RunState) afterReadyLocked() {
	if rs.phase != orbv1.PhasePreparing || !rs.allLocked(func(a agentProgress) bool { return a.ready }) {
		return
	}
	var failed []string
	for _, check := range rs.checks {
		if !check.Passed {
			failed = append(failed, check.Name+": "+check.Message)
		}
	}
	switch {
	case len(failed) > 0:
		rs.failLocked("registry checks failed: " + strings.Join(failed, "; "))
	case rs.Plan.CheckOnly:
		rs.phase = orbv1.PhaseSucceeded
		rs.message = "all registry checks passed"
		rs.collected = true // a check-only run has no results to collect
	default:
		rs.phase = orbv1.PhaseSeeding
	}
}

// afterScenarioDoneLocked starts the next scenario, or collects results
// after the last one.
func (rs *RunState) afterScenarioDoneLocked() {
	if rs.phase != orbv1.PhaseRunning {
		return
	}
	current := rs.scenario
	if !rs.allLocked(func(a agentProgress) bool { return a.doneScenario >= current }) {
		return
	}
	if current+1 < len(rs.Plan.Spec.Scenarios) {
		rs.startScenarioLocked(current + 1)
		return
	}
	rs.phase = orbv1.PhaseCollecting
}

func (rs *RunState) startScenarioLocked(index int) {
	rs.phase = orbv1.PhaseRunning
	rs.scenario = index
	rs.startAt = rs.now().Add(rs.startDelay)
}

// RequestAbort asks every agent to stop (RUN-6).
func (rs *RunState) RequestAbort() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.abortRequested || rs.phase.IsFinished() || rs.phase == orbv1.PhaseCollecting || rs.phase == orbv1.PhaseCleaningUp {
		return
	}
	rs.abortRequested = true
	rs.abortedAt = rs.now()
	rs.message = "aborted by user"
}

// checkAbortTimeoutLocked collects results if agents do not confirm an
// abort in time.
func (rs *RunState) checkAbortTimeoutLocked() {
	if rs.abortRequested && !rs.phase.IsFinished() && rs.phase != orbv1.PhaseCollecting &&
		rs.now().Sub(rs.abortedAt) > abortGracePeriod {
		rs.phase = orbv1.PhaseCollecting
	}
}

// Fail marks the run as failed, for example when an agent pod fails.
func (rs *RunState) Fail(message string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.failLocked(message)
}

func (rs *RunState) failLocked(message string) {
	if rs.phase.IsFinished() {
		return
	}
	rs.phase = orbv1.PhaseFailed
	rs.message = message
}

// MarkCollected is called after the result has been written. The run moves
// on to cleanup, or finishes.
func (rs *RunState) MarkCollected() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.collected = true
	rs.aggregator = nil // release the merged histograms

	switch {
	case rs.phase == orbv1.PhaseFailed:
		// Stays failed; cleanup needs working agents.
	case rs.abortRequested:
		rs.phase = orbv1.PhaseAborted
	case rs.Plan.Spec.Cleanup == orbv1.CleanupDelete:
		rs.phase = orbv1.PhaseCleaningUp
	default:
		rs.phase = orbv1.PhaseSucceeded
		rs.cleanup = "kept all pushed artifacts (cleanup policy Keep)"
	}
}

func (rs *RunState) allLocked(done func(agentProgress) bool) bool {
	for _, a := range rs.agents {
		if !done(a) {
			return false
		}
	}
	return true
}

// Snapshot is a copy of the state, used to update the run's status.
type Snapshot struct {
	Phase           orbv1.RunPhase
	Scenario        int
	AgentsReady     int
	Message         string
	Aborted         bool
	Collected       bool
	Checks          []orbv1.CheckResult
	ResolvedImages  []orbv1.ResolvedImage
	RegistryVersion string
	Cleanup         string
}

// Snapshot returns a copy of the current state.
func (rs *RunState) Snapshot() Snapshot {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.checkAbortTimeoutLocked()

	ready := 0
	for _, a := range rs.agents {
		if a.ready {
			ready++
		}
	}
	return Snapshot{
		Phase:           rs.phase,
		Scenario:        rs.scenario,
		AgentsReady:     ready,
		Message:         rs.message,
		Aborted:         rs.abortRequested,
		Collected:       rs.collected,
		Checks:          append([]orbv1.CheckResult(nil), rs.checks...),
		ResolvedImages:  append([]orbv1.ResolvedImage(nil), rs.resolvedImages...),
		RegistryVersion: rs.registryVersion,
		Cleanup:         rs.cleanup,
	}
}
