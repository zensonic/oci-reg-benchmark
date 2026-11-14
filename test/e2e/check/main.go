// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Command check verifies the results of the end-to-end test run. It reads
// the OrbBenchmarkResult (as JSON from "orbctl result get -o json") and the
// combined export (from "orbctl result export -o json") and fails if an
// expected operation is missing, had no successful operations, or the run
// did not complete.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// expected lists the operations each scenario of test/e2e/benchmark.yaml must report.
var expected = map[string][]string{
	"load":  {"pull", "push", "headManifest", "headBlob", "tag", "listTags", "catalog"},
	"dedup": {"existenceCheck", "crossRepoMount", "reupload", "baseline"},
}

func main() {
	if len(os.Args) != 3 {
		fail("usage: check RESULT.json EXPORT.json")
	}

	var result orbv1.OrbBenchmarkResult
	readJSON(os.Args[1], &result)
	checkSummary(result.Summary)

	var export struct {
		Summary orbv1.ResultSummary `json:"summary"`
		Detail  protocol.Detail     `json:"detail"`
	}
	readJSON(os.Args[2], &export)
	checkDetail(export.Detail)

	fmt.Println("end-to-end results look good")
}

func checkSummary(s orbv1.ResultSummary) {
	if s.Aborted {
		fail("the run was aborted")
	}
	for scenario, operations := range expected {
		sc := findScenario(s.Scenarios, scenario)
		if sc == nil {
			fail("scenario %q is missing from the result", scenario)
		}
		for _, op := range operations {
			res := findOperation(sc.Operations, op)
			if res == nil {
				fail("scenario %q: operation %q is missing", scenario, op)
			}
			if res.Count-res.Errors <= 0 {
				fail("scenario %q: operation %q had no successful operations (errors: %v)", scenario, op, res.ErrorsByType)
			}
			if res.Latency.P50Millis <= 0 {
				fail("scenario %q: operation %q has no latency", scenario, op)
			}
			fmt.Printf("ok  %-6s %-15s count=%d errors=%d p50=%.2fms\n", scenario, op, res.Count, res.Errors, res.Latency.P50Millis)
		}
	}
	if s.Cleanup == "" {
		fail("cleanup did not report anything")
	}
}

func checkDetail(d protocol.Detail) {
	for scenario := range expected {
		found := false
		for _, sc := range d.Scenarios {
			if sc.Name == scenario {
				found = true
				if len(sc.Intervals) == 0 {
					fail("export: scenario %q has no intervals", scenario)
				}
			}
		}
		if !found {
			fail("export: scenario %q is missing", scenario)
		}
	}
}

func findScenario(list []orbv1.ScenarioResult, name string) *orbv1.ScenarioResult {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func findOperation(list []orbv1.OperationResult, name string) *orbv1.OperationResult {
	for i := range list {
		if list[i].Operation == name {
			return &list[i]
		}
	}
	return nil
}

func readJSON(path string, into any) {
	data, err := os.ReadFile(path)
	if err != nil {
		fail("%v", err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		fail("decoding %s: %v", path, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "e2e check failed: "+format+"\n", args...)
	os.Exit(1)
}
