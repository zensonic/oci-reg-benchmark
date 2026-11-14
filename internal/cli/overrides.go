// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

// overrideHelp lists the keys "orbctl run start --set" accepts (RUN-4).
const overrideHelp = `Supported --set keys:
  cleanup=Delete|Keep
  seed=<number>
  repositoryPrefix=<prefix>
  workersPerAgent=<number>
  stages.<scenario>=<duration>:<workers>,...   for example stages.load=30s:10,2m:50,30s:0`

// parseOverrides turns "--set key=value" flags into run overrides. It
// returns nil if there are no flags.
func parseOverrides(settings []string) (*orbv1.RunOverrides, error) {
	if len(settings) == 0 {
		return nil, nil
	}
	o := &orbv1.RunOverrides{}

	for _, setting := range settings {
		key, value, found := strings.Cut(setting, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("--set %q is not in key=value form\n%s", setting, overrideHelp)
		}

		switch {
		case key == "cleanup":
			policy := orbv1.CleanupPolicy(value)
			if policy != orbv1.CleanupDelete && policy != orbv1.CleanupKeep {
				return nil, fmt.Errorf("cleanup must be Delete or Keep, not %q", value)
			}
			o.Cleanup = &policy

		case key == "seed":
			seed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("seed must be a whole number, not %q", value)
			}
			o.Seed = &seed

		case key == "repositoryPrefix":
			if value == "" {
				return nil, fmt.Errorf("repositoryPrefix must not be empty")
			}
			prefix := value
			o.RepositoryPrefix = &prefix

		case key == "workersPerAgent":
			workers, err := strconv.Atoi(value)
			if err != nil || workers < 1 {
				return nil, fmt.Errorf("workersPerAgent must be a positive whole number, not %q", value)
			}
			o.WorkersPerAgent = &workers

		case strings.HasPrefix(key, "stages."):
			scenario := strings.TrimPrefix(key, "stages.")
			if scenario == "" {
				return nil, fmt.Errorf("--set %q names no scenario; use stages.<scenario>=<stages>", setting)
			}
			stages, err := parseStages(value)
			if err != nil {
				return nil, fmt.Errorf("stages for scenario %q: %w", scenario, err)
			}
			if o.Stages == nil {
				o.Stages = map[string][]orbv1.Stage{}
			}
			o.Stages[scenario] = stages

		default:
			return nil, fmt.Errorf("unknown --set key %q\n%s", key, overrideHelp)
		}
	}
	return o, nil
}

// parseStages parses a stage list such as "30s:10,2m:50,30s:0", where each
// entry is <duration>:<workers>.
func parseStages(value string) ([]orbv1.Stage, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("no stages given; use <duration>:<workers>,... for example 30s:10,2m:50")
	}

	var stages []orbv1.Stage
	for _, entry := range strings.Split(value, ",") {
		durationText, workersText, found := strings.Cut(strings.TrimSpace(entry), ":")
		if !found {
			return nil, fmt.Errorf("stage %q is not in <duration>:<workers> form", entry)
		}
		duration, err := time.ParseDuration(durationText)
		if err != nil || duration <= 0 {
			return nil, fmt.Errorf("stage %q has an invalid duration %q", entry, durationText)
		}
		workers, err := strconv.Atoi(workersText)
		if err != nil || workers < 0 {
			return nil, fmt.Errorf("stage %q has an invalid worker count %q", entry, workersText)
		}
		stages = append(stages, orbv1.Stage{Duration: metav1.Duration{Duration: duration}, Workers: workers})
	}
	return stages, nil
}
