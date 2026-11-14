// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package loadprofile

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

func newStage(d time.Duration, workers int) orbv1.Stage {
	return orbv1.Stage{Duration: metav1.Duration{Duration: d}, Workers: workers}
}

func TestRampHoldAndRampDown(t *testing.T) {
	p := New(orbv1.LoadProfile{Stages: []orbv1.Stage{
		newStage(10*time.Second, 100), // ramp 0 -> 100
		newStage(10*time.Second, 100), // hold
		newStage(10*time.Second, 0),   // ramp down
	}})

	cases := []struct {
		at        time.Duration
		workers   int
		stageWant int
	}{
		{0, 0, 0},
		{5 * time.Second, 50, 0},
		{10 * time.Second, 100, 1},
		{15 * time.Second, 100, 1},
		{25 * time.Second, 50, 2},
		{30 * time.Second, 0, 2},
	}
	for _, c := range cases {
		if got := p.TotalWorkersAt(c.at); got != c.workers {
			t.Errorf("workers at %v = %d, want %d", c.at, got, c.workers)
		}
		if got := p.StageAt(c.at); got != c.stageWant {
			t.Errorf("stage at %v = %d, want %d", c.at, got, c.stageWant)
		}
	}
	if p.Duration() != 30*time.Second {
		t.Errorf("duration %v, want 30s", p.Duration())
	}
}

func TestAgentShareAddsUp(t *testing.T) {
	for total := 0; total < 50; total++ {
		sum := 0
		for i := 0; i < 7; i++ {
			sum += AgentShare(total, 7, i)
		}
		if sum != total {
			t.Errorf("shares of %d add up to %d", total, sum)
		}
	}
}
