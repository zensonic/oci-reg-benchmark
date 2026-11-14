// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package scenario defines what a scenario type (sub-benchmark) must
// implement, and the environment it runs in (TYPE-4). Each scenario type
// lives in its own package, for example scenario/load and scenario/dedup.
package scenario

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/imageset"
	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/module"
)

// Scenario is one sub-benchmark of a run.
type Scenario interface {
	// Name is the scenario name from the spec.
	Name() string

	// Operations are the operation names the scenario uses. They are checked
	// against the registry module before the run (REG-7).
	Operations() []string

	// Repositories are all repositories the scenario may push to. They are
	// prepared before the run and deleted by cleanup (CLEAN-3).
	Repositories() []string

	// Seed pushes the seed content this agent is responsible for (IMG-10).
	// It is not measured.
	Seed(ctx context.Context) error

	// Prepare resolves what the workers need from the seeded content, for
	// example pool manifests. It runs before the start time and is not measured.
	Prepare(ctx context.Context) error

	// NewWorker creates one worker. id is unique within the agent and scenario.
	NewWorker(id int) Worker
}

// Worker runs operations one after another for one simulated client.
type Worker interface {
	// RunOnce performs one operation and returns its measurement.
	RunOnce(ctx context.Context) metrics.Result

	// Close releases the worker's connections.
	Close()
}

// Env is everything a scenario needs from the agent.
type Env struct {
	Plan *protocol.Plan

	// Agent is this agent's index; Agents the total number of agents.
	Agent  int
	Agents int

	// Module is the registry module (REG-4).
	Module module.Module

	// NewClient creates a new, independent client for the registry under
	// test, with its own connections and token cache.
	NewClient func() (*core.Client, error)

	// Credentials for the registry under test, used by module hooks.
	Credentials *core.Credentials

	// Random generates random images. It is always set; with a static image
	// set it is only used for the deduplication baseline.
	Random *imageset.Random

	// Static is the downloaded static image set, or nil.
	Static *imageset.StaticSet

	// Layout names the run's repositories.
	Layout Layout
}

// Spec returns the benchmark spec of the run.
func (e *Env) Spec() *orbv1.OrbBenchmarkSpec {
	return &e.Plan.Spec
}

// Owns reports whether this agent seeds pool item k (sharded seeding).
func (e *Env) Owns(k int) bool {
	return k%max(e.Agents, 1) == e.Agent
}

// WorkerRand returns a random number generator for one worker. It is
// seeded from the benchmark seed, so runs are reproducible (RUN-7).
func (e *Env) WorkerRand(scenarioName string, worker int) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(e.Spec().Seed), imageset.Key(scenarioName, e.Agent, worker)))
}

// Platform is the platform used when a pulled reference is an index.
func (e *Env) Platform() string {
	if s := e.Spec().ImageSet.Static; s != nil && s.Platform != "" {
		return s.Platform
	}
	return orbv1.DefaultPlatform
}

// Tag returns a unique tag for a worker's n-th push, for example "a0-w3-17".
func (e *Env) Tag(prefix string, worker, n int) string {
	return fmt.Sprintf("%sa%d-w%d-%d", prefix, e.Agent, worker, n)
}

// ClientHolder gives a worker its registry client. With connection reuse
// PerWorker one client is kept for the worker's lifetime. With PerOperation
// every operation gets a fresh client, with new connections and an empty
// token cache, like a newly started client process (LOAD-4).
type ClientHolder struct {
	env          *Env
	perOperation bool
	client       *core.Client
}

// NewClientHolder creates a holder for the scenario's connection reuse mode.
func NewClientHolder(env *Env, profile orbv1.LoadProfile) *ClientHolder {
	return &ClientHolder{env: env, perOperation: profile.ConnectionReuse == "PerOperation"}
}

// Get returns the client to use for the next operation, and a function to
// call when the operation is done.
func (h *ClientHolder) Get() (*core.Client, func(), error) {
	if h.perOperation {
		c, err := h.env.NewClient()
		if err != nil {
			return nil, nil, err
		}
		return c, c.Close, nil
	}
	if h.client == nil {
		c, err := h.env.NewClient()
		if err != nil {
			return nil, nil, err
		}
		h.client = c
	}
	return h.client, func() {}, nil
}

// Close releases the kept client.
func (h *ClientHolder) Close() {
	if h.client != nil {
		h.client.Close()
		h.client = nil
	}
}

// Finish builds the measurement of an operation. latencyEnd is the moment
// latency ends (section 8); if it is zero, the operation's end is used.
func Finish(operation string, timing *core.Timing, latencyEnd time.Time, err error) metrics.Result {
	end := time.Now()
	if latencyEnd.IsZero() || latencyEnd.After(end) {
		latencyEnd = end
	}
	res := metrics.Result{
		Operation: operation,
		Start:     timing.Start,
		Latency:   latencyEnd.Sub(timing.Start),
		Total:     end.Sub(timing.Start),
		Phases:    timing.Phases(),
	}
	if err != nil {
		res.Err = err
		res.ErrorType = core.ClassifyError(err)
	}
	return res
}

// Failed builds the measurement of an operation that could not start, for
// example because no client could be created.
func Failed(operation string, err error) metrics.Result {
	return Finish(operation, core.StartTiming(), time.Time{}, err)
}
