// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/agent"
	"github.com/zensonic/oci-reg-benchmark/internal/export"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/fakeregistry"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

// TestControllerAndAgentsTogether runs real agents against the real agent
// API, coordinator, aggregator and SQLite store, with an in-memory registry.
// It covers everything except the Kubernetes reconciler, which the kind
// end-to-end test covers. It catches protocol mismatches between the agent
// and the controller.
func TestControllerAndAgentsTogether(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reg := fakeregistry.Start(fakeregistry.Options{Username: "bench", Password: "secret"})
	defer reg.Close()
	credsFile := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, credsFile, `{"auths":{"`+reg.Listener.Addr().String()+`":{"username":"bench","password":"secret"}}}`)

	st, err := store.Open(filepath.Join(t.TempDir(), "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	spec := integrationSpec(reg.URL)
	spec.Default()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	plan := protocol.Plan{
		RunName:        "it-run",
		RunUID:         "uid-1",
		Namespace:      "default",
		BenchmarkName:  "it",
		Spec:           spec,
		Agents:         spec.AgentCount(),
		RepositoryRoot: "bench/it-run",
	}
	const token = "agent-token"
	coordinator := NewCoordinator()
	rs := coordinator.Start(plan, token, NewAggregator(st, plan))

	api := httptest.NewServer(newAPIHandler(coordinator, st, logr.Discard()))
	defer api.Close()

	// Start the agents.
	var wg sync.WaitGroup
	errs := make([]error, plan.Agents)
	for i := range plan.Agents {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errs[index] = agent.RunWithOptions(ctx, agent.Options{
				ControllerURL:           api.URL,
				RunUID:                  plan.RunUID,
				RunName:                 plan.RunName,
				Token:                   token,
				AgentIndex:              index,
				RegistryCredentialsFile: credsFile,
				ScratchDir:              t.TempDir(),
				PollInterval:            100 * time.Millisecond,
				Logf:                    t.Logf,
			})
		}(i)
	}

	// Play the reconciler's part: collect when the coordinator asks for it.
	// The aggregator is released after collection, so take the results first.
	var results []orbv1.ScenarioResult
	for {
		snap := rs.Snapshot()
		if snap.Phase == orbv1.PhaseCollecting && !snap.Collected {
			results = rs.Aggregator().ScenarioResults()
			rs.MarkCollected()
		}
		if snap.Phase.IsFinished() {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run did not finish; last snapshot %+v", rs.Snapshot())
		case <-time.After(100 * time.Millisecond):
		}
	}
	wg.Wait()

	snap := rs.Snapshot()
	if snap.Phase != orbv1.PhaseSucceeded {
		t.Fatalf("run ended in phase %s: %s", snap.Phase, snap.Message)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("agent %d: %v", i, err)
		}
	}
	if len(snap.Checks) == 0 {
		t.Error("no registry checks were reported")
	}
	if snap.Cleanup == "" {
		t.Error("cleanup reported nothing")
	}

	checkScenarioResults(t, results)

	detail, err := export.BuildDetail(st, plan.RunUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Scenarios) != 2 || len(detail.Scenarios[0].Intervals) == 0 {
		t.Errorf("export detail is incomplete: %d scenarios", len(detail.Scenarios))
	}
}

func checkScenarioResults(t *testing.T, results []orbv1.ScenarioResult) {
	t.Helper()
	want := map[string][]string{
		"load":  {"pull", "push", "headManifest", "headBlob", "tag", "listTags", "catalog"},
		"dedup": {"existenceCheck", "crossRepoMount", "reupload", "baseline"},
	}
	for _, sc := range results {
		ops := map[string]orbv1.OperationResult{}
		for _, op := range append(append([]orbv1.OperationResult{}, sc.Operations...), sc.Warmup...) {
			existing := ops[op.Operation]
			existing.Count += op.Count
			existing.Errors += op.Errors
			ops[op.Operation] = existing
		}
		for _, name := range want[sc.Name] {
			op, ok := ops[name]
			if !ok || op.Count == 0 {
				t.Errorf("scenario %s: no %s operations", sc.Name, name)
				continue
			}
			if op.Errors > 0 {
				t.Errorf("scenario %s: %d of %d %s operations failed", sc.Name, op.Errors, op.Count, name)
			}
		}
		delete(want, sc.Name)
	}
	if len(want) > 0 {
		t.Errorf("missing scenario results: %v", want)
	}
}

func integrationSpec(registryURL string) orbv1.OrbBenchmarkSpec {
	stage := func(d time.Duration, workers int) orbv1.Stage {
		return orbv1.Stage{Duration: metav1.Duration{Duration: d}, Workers: workers}
	}
	small := resource.MustParse("32Ki")
	return orbv1.OrbBenchmarkSpec{
		Registry: orbv1.RegistrySpec{URL: registryURL, Module: "generic", RepositoryPrefix: "bench"},
		ImageSet: orbv1.ImageSetSpec{
			Type:     orbv1.ImageSetRandom,
			Random:   &orbv1.RandomImageSet{Layers: 2, LayerSize: orbv1.SizeDistribution{Distribution: "Fixed", Size: &small}},
			PoolSize: 3,
		},
		Agents: orbv1.AgentSpec{WorkersPerAgent: 2},
		Scenarios: []orbv1.ScenarioSpec{
			{
				Name: "load",
				Type: orbv1.ScenarioLoad,
				Load: &orbv1.LoadScenario{
					Mix:      map[string]int{"pull": 30, "push": 20, "headManifest": 10, "headBlob": 10, "tag": 10, "listTags": 10, "catalog": 10},
					PageSize: 5,
				},
				Profile: orbv1.LoadProfile{
					Warmup: &metav1.Duration{Duration: time.Second},
					Stages: []orbv1.Stage{stage(2*time.Second, 4), stage(2*time.Second, 4)},
				},
			},
			{
				Name: "dedup",
				Type: orbv1.ScenarioDeduplication,
				Deduplication: &orbv1.DeduplicationScenario{
					Methods: []string{"existenceCheck", "crossRepoMount", "reupload"},
				},
				Profile: orbv1.LoadProfile{Stages: []orbv1.Stage{stage(3*time.Second, 4)}},
			},
		},
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
