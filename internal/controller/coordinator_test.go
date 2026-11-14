// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"
	"time"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

func testPlan(agents, scenarios int, cleanup orbv1.CleanupPolicy) protocol.Plan {
	spec := orbv1.OrbBenchmarkSpec{Cleanup: cleanup}
	for i := 0; i < scenarios; i++ {
		spec.Scenarios = append(spec.Scenarios, orbv1.ScenarioSpec{Name: string(rune('a' + i)), Type: orbv1.ScenarioLoad})
	}
	return protocol.Plan{RunUID: "uid", RunName: "run", Agents: agents, Spec: spec}
}

func newTestRunState(plan protocol.Plan) *RunState {
	rs := newRunState(plan, "token", nil)
	clock := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	rs.now = func() time.Time { return clock }
	return rs
}

func all(t *testing.T, rs *RunState, update protocol.StateUpdate) {
	t.Helper()
	for i := 0; i < rs.Plan.Agents; i++ {
		if err := rs.Update(i, update); err != nil {
			t.Fatal(err)
		}
	}
}

func wantPhase(t *testing.T, rs *RunState, want orbv1.RunPhase) {
	t.Helper()
	if got := rs.Snapshot().Phase; got != want {
		t.Fatalf("phase %s, want %s", got, want)
	}
}

func wantAction(t *testing.T, rs *RunState, agent int, want protocol.Action) protocol.Command {
	t.Helper()
	cmd := rs.Command(agent)
	if cmd.Action != want {
		t.Fatalf("agent %d got %s, want %s", agent, cmd.Action, want)
	}
	return cmd
}

func TestFullRunWithCleanup(t *testing.T) {
	rs := newTestRunState(testPlan(2, 2, orbv1.CleanupDelete))
	wantPhase(t, rs, orbv1.PhasePreparing)
	wantAction(t, rs, 0, protocol.ActionWait)

	if err := rs.Update(0, protocol.StateUpdate{State: protocol.StateReady, Checks: []orbv1.CheckResult{{Name: "ping", Passed: true}}}); err != nil {
		t.Fatal(err)
	}
	wantPhase(t, rs, orbv1.PhasePreparing) // agent 1 is not ready yet
	all(t, rs, protocol.StateUpdate{State: protocol.StateReady})
	wantPhase(t, rs, orbv1.PhaseSeeding)
	wantAction(t, rs, 1, protocol.ActionSeed)

	all(t, rs, protocol.StateUpdate{State: protocol.StateSeeded})
	wantPhase(t, rs, orbv1.PhaseRunning)
	cmd := wantAction(t, rs, 0, protocol.ActionRun)
	if cmd.Scenario != 0 || !cmd.StartAt.Equal(rs.now().Add(defaultStartDelay)) {
		t.Errorf("unexpected run command %+v", cmd)
	}

	// One agent finishes scenario 0 and waits for the other.
	_ = rs.Update(0, protocol.StateUpdate{State: protocol.StateScenarioDone, Scenario: 0})
	wantAction(t, rs, 0, protocol.ActionWait)
	wantAction(t, rs, 1, protocol.ActionRun)
	_ = rs.Update(1, protocol.StateUpdate{State: protocol.StateScenarioDone, Scenario: 0})
	cmd = wantAction(t, rs, 0, protocol.ActionRun)
	if cmd.Scenario != 1 {
		t.Errorf("expected scenario 1, got %d", cmd.Scenario)
	}

	all(t, rs, protocol.StateUpdate{State: protocol.StateScenarioDone, Scenario: 1})
	wantPhase(t, rs, orbv1.PhaseCollecting)
	wantAction(t, rs, 0, protocol.ActionWait)

	rs.MarkCollected()
	wantPhase(t, rs, orbv1.PhaseCleaningUp)
	wantAction(t, rs, 0, protocol.ActionCleanup)
	wantAction(t, rs, 1, protocol.ActionExit)

	_ = rs.Update(0, protocol.StateUpdate{State: protocol.StateCleanedUp, Message: "deleted 4 repositories"})
	wantPhase(t, rs, orbv1.PhaseSucceeded)
	wantAction(t, rs, 0, protocol.ActionExit)
	if rs.Snapshot().Cleanup != "deleted 4 repositories" {
		t.Errorf("cleanup message not kept")
	}
}

func TestKeepSkipsCleanup(t *testing.T) {
	rs := newTestRunState(testPlan(1, 1, orbv1.CleanupKeep))
	all(t, rs, protocol.StateUpdate{State: protocol.StateReady})
	all(t, rs, protocol.StateUpdate{State: protocol.StateSeeded})
	all(t, rs, protocol.StateUpdate{State: protocol.StateScenarioDone, Scenario: 0})
	rs.MarkCollected()
	wantPhase(t, rs, orbv1.PhaseSucceeded)
}

func TestAbort(t *testing.T) {
	rs := newTestRunState(testPlan(2, 1, orbv1.CleanupDelete))
	all(t, rs, protocol.StateUpdate{State: protocol.StateReady})
	all(t, rs, protocol.StateUpdate{State: protocol.StateSeeded})

	rs.RequestAbort()
	wantAction(t, rs, 0, protocol.ActionAbort)
	_ = rs.Update(0, protocol.StateUpdate{State: protocol.StateAborted})
	wantAction(t, rs, 0, protocol.ActionWait)
	wantPhase(t, rs, orbv1.PhaseRunning)
	_ = rs.Update(1, protocol.StateUpdate{State: protocol.StateAborted})
	wantPhase(t, rs, orbv1.PhaseCollecting)

	rs.MarkCollected()
	wantPhase(t, rs, orbv1.PhaseAborted)
	if !rs.Snapshot().Aborted {
		t.Error("snapshot does not say aborted")
	}
	wantAction(t, rs, 1, protocol.ActionExit)
}

func TestAbortTimesOut(t *testing.T) {
	rs := newTestRunState(testPlan(1, 1, orbv1.CleanupDelete))
	now := rs.now()
	rs.RequestAbort()
	rs.now = func() time.Time { return now.Add(abortGracePeriod + time.Second) }
	wantPhase(t, rs, orbv1.PhaseCollecting)
}

func TestAgentFailure(t *testing.T) {
	rs := newTestRunState(testPlan(2, 1, orbv1.CleanupDelete))
	_ = rs.Update(1, protocol.StateUpdate{State: protocol.StateFailed, Message: "out of disk"})
	wantPhase(t, rs, orbv1.PhaseFailed)
	wantAction(t, rs, 0, protocol.ActionAbort)
	if got := rs.Snapshot().Message; got != "agent 1 failed: out of disk" {
		t.Errorf("message %q", got)
	}
	rs.MarkCollected()
	wantPhase(t, rs, orbv1.PhaseFailed)
}

func TestCheckOnly(t *testing.T) {
	plan := testPlan(1, 1, orbv1.CleanupDelete)
	plan.CheckOnly = true

	ok := newTestRunState(plan)
	_ = ok.Update(0, protocol.StateUpdate{State: protocol.StateReady, Checks: []orbv1.CheckResult{{Name: "ping", Passed: true}}})
	wantPhase(t, ok, orbv1.PhaseSucceeded)

	bad := newTestRunState(plan)
	_ = bad.Update(0, protocol.StateUpdate{State: protocol.StateReady, Checks: []orbv1.CheckResult{{Name: "push", Passed: false, Message: "denied"}}})
	wantPhase(t, bad, orbv1.PhaseFailed)
}

func TestFailedStateKeepsChecks(t *testing.T) {
	plan := testPlan(1, 1, orbv1.CleanupDelete)
	plan.CheckOnly = true
	rs := newTestRunState(plan)
	_ = rs.Update(0, protocol.StateUpdate{
		State: protocol.StateFailed, Message: "registry checks failed",
		Checks: []orbv1.CheckResult{{Name: "push", Passed: false, Message: "denied"}},
	})
	snap := rs.Snapshot()
	if snap.Phase != orbv1.PhaseFailed || len(snap.Checks) != 1 || snap.Checks[0].Message != "denied" {
		t.Errorf("checks from a failed update were not kept: %+v", snap)
	}
}

func TestCleanupFailureStillFinishes(t *testing.T) {
	rs := newTestRunState(testPlan(1, 1, orbv1.CleanupDelete))
	all(t, rs, protocol.StateUpdate{State: protocol.StateReady})
	all(t, rs, protocol.StateUpdate{State: protocol.StateSeeded})
	all(t, rs, protocol.StateUpdate{State: protocol.StateScenarioDone, Scenario: 0})
	rs.MarkCollected()
	_ = rs.Update(0, protocol.StateUpdate{State: protocol.StateCleanedUp, Message: "cleanup failed: forbidden"})
	wantPhase(t, rs, orbv1.PhaseSucceeded)
	if rs.Snapshot().Cleanup != "cleanup failed: forbidden" {
		t.Errorf("cleanup message %q", rs.Snapshot().Cleanup)
	}
}

func TestFailedChecksStopNormalRun(t *testing.T) {
	rs := newTestRunState(testPlan(1, 1, orbv1.CleanupDelete))
	_ = rs.Update(0, protocol.StateUpdate{State: protocol.StateReady, Checks: []orbv1.CheckResult{{Name: "catalog", Passed: false, Message: "unsupported"}}})
	wantPhase(t, rs, orbv1.PhaseFailed)
}

func TestUpdateRejectsBadInput(t *testing.T) {
	rs := newTestRunState(testPlan(1, 1, orbv1.CleanupDelete))
	if err := rs.Update(5, protocol.StateUpdate{State: protocol.StateReady}); err == nil {
		t.Error("out-of-range agent accepted")
	}
	if err := rs.Update(0, protocol.StateUpdate{State: "dancing"}); err == nil {
		t.Error("unknown state accepted")
	}
}
