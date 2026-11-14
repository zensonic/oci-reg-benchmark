// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package loadprofile works out how many workers should be active at any
// moment of a scenario (LOAD-1), and how that total is split across agents
// (LOAD-2).
package loadprofile

import (
	"math"
	"time"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

// Profile is a load profile: stages that run one after another. Within a
// stage the worker count changes linearly from the previous stage's count to
// the stage's own count. Before the first stage the count is 0.
type Profile struct {
	stages []stage
	warmup time.Duration
}

type stage struct {
	start, end             time.Duration
	fromWorkers, toWorkers int
}

// New builds a profile from the API stages.
func New(spec orbv1.LoadProfile) Profile {
	p := Profile{}
	if spec.Warmup != nil {
		p.warmup = spec.Warmup.Duration
	}
	var elapsed time.Duration
	previous := 0
	for _, s := range spec.Stages {
		p.stages = append(p.stages, stage{
			start:       elapsed,
			end:         elapsed + s.Duration.Duration,
			fromWorkers: previous,
			toWorkers:   s.Workers,
		})
		elapsed += s.Duration.Duration
		previous = s.Workers
	}
	return p
}

// Duration is the total length of all stages.
func (p Profile) Duration() time.Duration {
	if len(p.stages) == 0 {
		return 0
	}
	return p.stages[len(p.stages)-1].end
}

// Warmup is the length of the warm-up period at the start (LOAD-5).
func (p Profile) Warmup() time.Duration {
	return p.warmup
}

// IsWarmup reports whether the moment is inside the warm-up period.
func (p Profile) IsWarmup(elapsed time.Duration) bool {
	return elapsed < p.warmup
}

// StageAt returns the index of the stage running at the given moment. After
// the last stage it returns the last index.
func (p Profile) StageAt(elapsed time.Duration) int {
	for i, s := range p.stages {
		if elapsed < s.end {
			return i
		}
	}
	return max(len(p.stages)-1, 0)
}

// StageWorkers returns the target worker count of each stage.
func (p Profile) StageWorkers() []int {
	counts := make([]int, len(p.stages))
	for i, s := range p.stages {
		counts[i] = s.toWorkers
	}
	return counts
}

// StageDuration returns the length of a stage.
func (p Profile) StageDuration(index int) time.Duration {
	if index < 0 || index >= len(p.stages) {
		return 0
	}
	return p.stages[index].end - p.stages[index].start
}

// TotalWorkersAt returns how many workers should be active across all agents
// at the given moment.
func (p Profile) TotalWorkersAt(elapsed time.Duration) int {
	if elapsed < 0 || len(p.stages) == 0 {
		return 0
	}
	for _, s := range p.stages {
		if elapsed >= s.end {
			continue
		}
		length := s.end - s.start
		if length <= 0 {
			return s.toWorkers
		}
		progress := float64(elapsed-s.start) / float64(length)
		count := float64(s.fromWorkers) + progress*float64(s.toWorkers-s.fromWorkers)
		return int(math.Round(count))
	}
	return 0 // the profile is over
}

// AgentShare returns how many of total workers the agent with the given
// index runs. Workers are spread as evenly as possible; lower indexes get
// the remainder.
func AgentShare(total, agents, index int) int {
	if agents <= 0 {
		return 0
	}
	share := total / agents
	if index < total%agents {
		share++
	}
	return share
}
