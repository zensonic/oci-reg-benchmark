// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

// Layout names the repositories of a run (IMG-12). See the repository layout
// section of docs/architecture.md.
type Layout struct {
	// Root is <repositoryPrefix>/<run name>.
	Root string
	// RunName is the run's name.
	RunName string
}

// Pool is the repository with seeded images for pull, HEAD, tag and tag listing.
func (l Layout) Pool(scenario string) string { return l.Root + "/" + scenario + "/pool" }

// Push is the repository for load-scenario pushes.
func (l Layout) Push(scenario string) string { return l.Root + "/" + scenario + "/push" }

// Source is the deduplication repository with the existing content.
func (l Layout) Source(scenario string) string { return l.Root + "/" + scenario + "/source" }

// Target is the repository deduplication pushes go to, which depends on the
// scope (DEDUP-3).
func (l Layout) Target(scenario string, d *orbv1.DeduplicationScenario) string {
	switch d.Scope {
	case orbv1.DedupScopeSameRepository:
		return l.Source(scenario)
	case orbv1.DedupScopeDifferentProject:
		return d.TargetProject + "/" + l.RunName + "/" + scenario + "/target"
	default:
		return l.Root + "/" + scenario + "/target"
	}
}
