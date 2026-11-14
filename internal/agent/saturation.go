// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"math"
	"runtime"
	runtimemetrics "runtime/metrics"
	"syscall"
	"time"
)

// saturationSampler measures whether the agent itself is the bottleneck
// (LOAD-9): how much CPU it uses and how long goroutines wait to be
// scheduled.
type saturationSampler struct {
	lastWall time.Time
	lastCPU  time.Duration

	schedSample []runtimemetrics.Sample
	lastCounts  []uint64
}

const schedLatencies = "/sched/latencies:seconds"

func newSaturationSampler() *saturationSampler {
	s := &saturationSampler{
		schedSample: []runtimemetrics.Sample{{Name: schedLatencies}},
	}
	s.lastWall, s.lastCPU = time.Now(), processCPU()
	runtimemetrics.Read(s.schedSample)
	if h := s.histogram(); h != nil {
		s.lastCounts = append([]uint64{}, h.Counts...)
	}
	return s
}

// Sample returns the CPU utilization (0 to 1, relative to GOMAXPROCS) and
// the 99th percentile scheduler delay in milliseconds since the last call.
func (s *saturationSampler) Sample() (cpuUtilization, schedP99Millis float64) {
	now, cpu := time.Now(), processCPU()
	wall := now.Sub(s.lastWall)
	if wall > 0 {
		cpuUtilization = float64(cpu-s.lastCPU) / (float64(wall) * float64(runtime.GOMAXPROCS(0)))
		cpuUtilization = math.Min(math.Max(cpuUtilization, 0), 1)
	}
	s.lastWall, s.lastCPU = now, cpu

	runtimemetrics.Read(s.schedSample)
	if h := s.histogram(); h != nil {
		schedP99Millis = deltaQuantile(h, s.lastCounts, 0.99) * 1000
		s.lastCounts = append(s.lastCounts[:0], h.Counts...)
	}
	return cpuUtilization, math.Round(schedP99Millis*1000) / 1000
}

func (s *saturationSampler) histogram() *runtimemetrics.Float64Histogram {
	if s.schedSample[0].Value.Kind() != runtimemetrics.KindFloat64Histogram {
		return nil
	}
	return s.schedSample[0].Value.Float64Histogram()
}

// deltaQuantile computes a quantile (in seconds) of the values added to a
// cumulative runtime histogram since the previous counts were taken.
func deltaQuantile(h *runtimemetrics.Float64Histogram, previous []uint64, quantile float64) float64 {
	var total uint64
	delta := make([]uint64, len(h.Counts))
	for i, c := range h.Counts {
		if i < len(previous) {
			c -= previous[i]
		}
		delta[i] = c
		total += c
	}
	if total == 0 {
		return 0
	}
	rank := uint64(math.Ceil(quantile * float64(total)))
	var seen uint64
	for i, c := range delta {
		seen += c
		if seen >= rank {
			upper := h.Buckets[i+1]
			if math.IsInf(upper, 1) {
				upper = h.Buckets[i]
			}
			return upper
		}
	}
	return 0
}

// processCPU returns the CPU time used by this process (user plus system).
func processCPU() time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}
