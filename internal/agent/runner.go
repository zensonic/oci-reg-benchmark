// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zensonic/oci-reg-benchmark/internal/loadprofile"
	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// resizeEvery is how often the worker count follows the load profile.
const resizeEvery = 100 * time.Millisecond

// runScenario runs one scenario: it resolves the seeded content, waits for
// the shared start time (LOAD-3), follows the load profile, and sends one
// report per measurement interval. It returns aborted=true if the controller
// aborted the run.
func (a *agent) runScenario(ctx context.Context, index int, startAt time.Time) (aborted bool, err error) {
	sc := a.scenarios[index]
	spec := a.plan.Spec.Scenarios[index]

	if err := sc.Prepare(ctx); err != nil {
		return false, fmt.Errorf("preparing scenario %q: %w", sc.Name(), err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var abortRequested atomic.Bool
	go a.watchForAbort(runCtx, &abortRequested, cancel)

	a.logf("scenario %s starts at %s", sc.Name(), startAt.Format(time.RFC3339Nano))
	select {
	case <-time.After(time.Until(startAt)):
	case <-runCtx.Done():
	}

	profile := loadprofile.New(spec.Profile)
	recorder := metrics.NewRecorder(a.plan.Spec.Measurement.RawSamples, startAt)
	var think time.Duration
	if spec.Profile.ThinkTime != nil {
		think = spec.Profile.ThinkTime.Duration
	}
	pool := newWorkerPool(runCtx, sc, recorder, think, spec.Profile.OperationTimeout.Duration)

	rep := &reporter{
		agent:    a,
		scenario: index,
		name:     sc.Name(),
		profile:  profile,
		recorder: recorder,
		pool:     pool,
		startAt:  startAt,
		interval: a.plan.Spec.Measurement.Interval.Duration,
		sampler:  newSaturationSampler(),
		stop:     make(chan struct{}),
		next:     0,
	}
	var reporting sync.WaitGroup
	reporting.Add(1)
	go func() {
		defer reporting.Done()
		rep.loop(ctx)
	}()

	ticker := time.NewTicker(resizeEvery)
	defer ticker.Stop()
	for runCtx.Err() == nil {
		elapsed := time.Since(startAt)
		if elapsed >= profile.Duration() {
			break
		}
		pool.Resize(loadprofile.AgentShare(profile.TotalWorkersAt(elapsed), a.env.Agents, a.env.Agent))
		select {
		case <-ticker.C:
		case <-runCtx.Done():
		}
	}

	// Let running operations finish (they are bounded by the operation
	// timeout, or cut short by an abort), then send the last report.
	pool.Resize(0)
	pool.Wait()
	close(rep.stop)
	reporting.Wait()
	rep.flush(ctx, true)

	a.logf("scenario %s finished", sc.Name())
	return abortRequested.Load(), nil
}

// watchForAbort polls the controller while a scenario runs and cancels the
// run if it is aborted (RUN-6).
func (a *agent) watchForAbort(ctx context.Context, aborted *atomic.Bool, cancel context.CancelFunc) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.opts.PollInterval):
		}
		cmd, err := a.control.Command(ctx)
		if err == nil && cmd.Action == protocol.ActionAbort {
			aborted.Store(true)
			cancel()
			return
		}
	}
}

// reporter sends one IntervalReport per measurement interval, aligned to the
// scenario's shared start time, so interval numbers match across agents.
type reporter struct {
	agent    *agent
	scenario int
	name     string
	profile  loadprofile.Profile
	recorder *metrics.Recorder
	pool     *workerPool
	startAt  time.Time
	interval time.Duration
	sampler  *saturationSampler
	stop     chan struct{}

	next int // the next interval number to report
}

func (r *reporter) loop(ctx context.Context) {
	for {
		boundary := r.startAt.Add(time.Duration(r.next+1) * r.interval)
		select {
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		case <-time.After(time.Until(boundary)):
		}
		r.flush(ctx, false)
	}
}

// flush sends the results collected since the last report. The final flush
// after a scenario is sent even if it is empty, so the controller sees the
// last worker count.
func (r *reporter) flush(ctx context.Context, final bool) {
	interval := r.next
	if final {
		interval = max(r.next, int(time.Since(r.startAt)/r.interval))
	}
	r.next = interval + 1

	operations, samples, err := r.recorder.Flush()
	if err != nil {
		r.agent.logf("building the interval report: %v", err)
		return
	}
	if final && len(operations) == 0 && len(samples) == 0 {
		return
	}

	offset := time.Duration(interval) * r.interval
	cpu, sched := r.sampler.Sample()
	report := protocol.IntervalReport{
		Scenario:            r.scenario,
		ScenarioName:        r.name,
		Interval:            interval,
		Stage:               r.profile.StageAt(offset),
		Warmup:              r.profile.IsWarmup(offset),
		Workers:             r.pool.Size(),
		CPUUtilization:      cpu,
		SchedDelayP99Millis: sched,
		Operations:          operations,
		Samples:             samples,
	}
	err = retry(ctx, 10*time.Second, func() error { return r.agent.control.Report(ctx, report) })
	if err != nil {
		r.agent.logf("sending the report for interval %d: %v", interval, err)
	}
}
