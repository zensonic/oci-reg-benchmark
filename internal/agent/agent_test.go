// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/imageset"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/fakeregistry"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/generic"
	"github.com/zensonic/oci-reg-benchmark/internal/scenario"
	"github.com/zensonic/oci-reg-benchmark/internal/scenario/dedup"
)

const testToken = "agent-token"

// fakeController implements the controller side of the agent API with the
// command sequence from docs/architecture.md.
type fakeController struct {
	t      *testing.T
	plan   protocol.Plan
	agents int

	mu         sync.Mutex
	phase      string // preparing, seeding, running, cleanup, done
	scenario   int
	startAt    time.Time
	ready      map[int]bool
	seeded     map[int]bool
	done       map[int]bool // agents done with the current scenario
	cleanupMsg string
	reports    []protocol.IntervalReport
	failures   []string
}

func newFakeController(t *testing.T, plan protocol.Plan) *fakeController {
	return &fakeController{
		t: t, plan: plan, agents: plan.Agents, phase: "preparing",
		ready: map[int]bool{}, seeded: map[int]bool{}, done: map[int]bool{},
	}
}

func (c *fakeController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// agent/v1/runs/{uid}/plan  or  agent/v1/runs/{uid}/agents/{i}/{what}
	if len(parts) == 5 && parts[4] == "plan" {
		_ = json.NewEncoder(w).Encode(c.plan)
		return
	}
	if len(parts) != 7 {
		http.NotFound(w, r)
		return
	}
	var agent int
	_ = json.Unmarshal([]byte(parts[5]), &agent)

	c.mu.Lock()
	defer c.mu.Unlock()
	switch parts[6] {
	case "command":
		_ = json.NewEncoder(w).Encode(c.command(agent))
	case "state":
		var update protocol.StateUpdate
		_ = json.NewDecoder(r.Body).Decode(&update)
		c.handleState(agent, update)
	case "report":
		var report protocol.IntervalReport
		_ = json.NewDecoder(r.Body).Decode(&report)
		c.reports = append(c.reports, report)
	}
}

func (c *fakeController) command(agent int) protocol.Command {
	switch c.phase {
	case "seeding":
		return protocol.Command{Action: protocol.ActionSeed}
	case "running":
		if c.done[agent] {
			return protocol.Command{Action: protocol.ActionWait}
		}
		return protocol.Command{Action: protocol.ActionRun, Scenario: c.scenario, StartAt: c.startAt}
	case "cleanup":
		if agent == 0 {
			return protocol.Command{Action: protocol.ActionCleanup}
		}
		return protocol.Command{Action: protocol.ActionExit}
	case "done":
		return protocol.Command{Action: protocol.ActionExit}
	default:
		return protocol.Command{Action: protocol.ActionWait}
	}
}

func (c *fakeController) handleState(agent int, update protocol.StateUpdate) {
	switch update.State {
	case protocol.StateReady:
		c.ready[agent] = true
		if len(c.ready) == c.agents {
			c.phase = "seeding"
		}
	case protocol.StateSeeded:
		c.seeded[agent] = true
		if len(c.seeded) == c.agents {
			c.startScenario(0)
		}
	case protocol.StateScenarioDone:
		c.done[agent] = true
		if len(c.done) == c.agents {
			if c.scenario+1 < len(c.plan.Spec.Scenarios) {
				c.startScenario(c.scenario + 1)
			} else {
				c.phase = "cleanup"
			}
		}
	case protocol.StateCleanedUp:
		c.cleanupMsg = update.Message
		c.phase = "done"
	case protocol.StateFailed, protocol.StateAborted:
		c.failures = append(c.failures, string(update.State)+": "+update.Message)
		c.phase = "done"
	}
}

func (c *fakeController) startScenario(index int) {
	c.phase, c.scenario = "running", index
	c.startAt = time.Now().Add(300 * time.Millisecond)
	c.done = map[int]bool{}
}

func seconds(s float64) metav1.Duration {
	return metav1.Duration{Duration: time.Duration(s * float64(time.Second))}
}

func quantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

// testSpec has a load scenario with all seven operations and a
// deduplication scenario with all three methods, with short stages.
func testSpec(registryURL string) orbv1.OrbBenchmarkSpec {
	spec := orbv1.OrbBenchmarkSpec{
		Registry: orbv1.RegistrySpec{URL: registryURL, Module: "generic", RepositoryPrefix: "bench"},
		ImageSet: orbv1.ImageSetSpec{
			Type:     orbv1.ImageSetRandom,
			PoolSize: 3,
			Random:   &orbv1.RandomImageSet{Layers: 2, LayerSize: orbv1.SizeDistribution{Distribution: "Fixed", Size: quantity("32Ki")}},
		},
		Scenarios: []orbv1.ScenarioSpec{
			{
				Name: "load",
				Type: orbv1.ScenarioLoad,
				Load: &orbv1.LoadScenario{
					Mix: map[string]int{
						orbv1.OpPull: 30, orbv1.OpPush: 20, orbv1.OpHeadManifest: 10, orbv1.OpHeadBlob: 10,
						orbv1.OpTag: 10, orbv1.OpListTags: 10, orbv1.OpCatalog: 10,
					},
					ChunkSize: quantity("16Ki"),
					PageSize:  2,
				},
				Profile: orbv1.LoadProfile{Stages: []orbv1.Stage{
					{Duration: seconds(1.5), Workers: 4},
					{Duration: seconds(1), Workers: 4},
				}},
			},
			{
				Name: "dedup",
				Type: orbv1.ScenarioDeduplication,
				Deduplication: &orbv1.DeduplicationScenario{
					Scope:     orbv1.DedupScopeSameProject,
					Methods:   []string{orbv1.DedupExistenceCheck, orbv1.DedupCrossRepoMount, orbv1.DedupReupload},
					ChunkSize: quantity("16Ki"),
				},
				Profile: orbv1.LoadProfile{Stages: []orbv1.Stage{{Duration: seconds(2), Workers: 4}}},
			},
		},
		Measurement: orbv1.MeasurementSpec{Interval: &metav1.Duration{Duration: 500 * time.Millisecond}},
	}
	spec.Default()
	return spec
}

func TestAgentsRunAFullBenchmark(t *testing.T) {
	reg := fakeregistry.Start(fakeregistry.Options{Username: "bench", Password: "secret"})
	defer reg.Close()

	// Registry credentials, as mounted from the dockerconfigjson Secret.
	credsDir := t.TempDir()
	credsFile := filepath.Join(credsDir, ".dockerconfigjson")
	host := strings.TrimPrefix(reg.URL, "http://")
	writeFile(t, credsFile, `{"auths":{"`+host+`":{"username":"bench","password":"secret"}}}`)

	plan := protocol.Plan{
		RunName: "run-1", RunUID: "uid-1", BenchmarkName: "bench", Agents: 2,
		RepositoryRoot: "bench/run-1", Spec: testSpec(reg.URL),
	}
	ctrl := newFakeController(t, plan)
	server := httptest.NewServer(ctrl)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, plan.Agents)
	for i := 0; i < plan.Agents; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errs[index] = RunWithOptions(ctx, Options{
				ControllerURL:           server.URL,
				RunUID:                  plan.RunUID,
				RunName:                 plan.RunName,
				Token:                   testToken,
				AgentIndex:              index,
				RegistryCredentialsFile: credsFile,
				SourceCredentialsFile:   filepath.Join(credsDir, "missing"),
				CAFile:                  filepath.Join(credsDir, "missing"),
				ScratchDir:              t.TempDir(),
				PollInterval:            50 * time.Millisecond,
				Logf:                    t.Logf,
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("agent %d: %v", i, err)
		}
	}
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if len(ctrl.failures) > 0 {
		t.Fatalf("agents reported failures: %v", ctrl.failures)
	}

	counts := map[string]int64{}
	errorsByOp := map[string]map[string]string{}
	mountMisses := int64(0)
	for _, r := range ctrl.reports {
		for _, op := range r.Operations {
			counts[r.ScenarioName+"/"+op.Operation] += op.Count
			if len(op.Errors) > 0 {
				errorsByOp[r.ScenarioName+"/"+op.Operation] = op.ErrorExamples
			}
			mountMisses += op.Counters[dedup.CounterMountMisses]
			if op.Count > len64(op.Errors) && op.Histograms[protocol.MetricLatency] == nil {
				t.Errorf("%s has no latency histogram", op.Operation)
			}
		}
	}
	for _, op := range []string{"load/pull", "load/push", "load/headManifest", "load/headBlob", "load/tag",
		"load/listTags", "load/catalog", "dedup/existenceCheck", "dedup/crossRepoMount", "dedup/reupload", "dedup/baseline"} {
		if counts[op] == 0 {
			t.Errorf("no %s operations were reported (counts: %v)", op, counts)
		}
	}
	if len(errorsByOp) > 0 {
		t.Errorf("operations failed: %v", errorsByOp)
	}
	if mountMisses != 0 {
		t.Errorf("%d mount misses; mounts within a project should succeed", mountMisses)
	}

	if !strings.HasPrefix(ctrl.cleanupMsg, "deleted") {
		t.Errorf("cleanup message %q", ctrl.cleanupMsg)
	}
	if reg.HasManifest("bench/run-1/load/pool", "img-0") {
		t.Error("cleanup left the pool image behind")
	}
}

func len64(m map[string]int64) int64 {
	var total int64
	for _, v := range m {
		total += v
	}
	return total
}

func TestMountMissFallsBackToUpload(t *testing.T) {
	reg := fakeregistry.Start(fakeregistry.Options{RefuseMounts: true})
	defer reg.Close()

	spec := testSpec(reg.URL)
	no := false
	spec.Scenarios[1].Deduplication.Methods = []string{orbv1.DedupCrossRepoMount}
	spec.Scenarios[1].Deduplication.Baseline = &no

	plan := &protocol.Plan{RunName: "run-2", Agents: 1, RepositoryRoot: "bench/run-2", Spec: spec}
	env := &scenario.Env{
		Plan: plan, Agents: 1, Module: generic.Module{},
		Random: imageset.NewRandom(spec.ImageSet.Random, spec.Seed),
		Layout: scenario.Layout{Root: plan.RepositoryRoot, RunName: plan.RunName},
		NewClient: func() (*core.Client, error) {
			return core.New(core.Config{BaseURL: reg.URL})
		},
	}
	sc, err := dedup.New(env, spec.Scenarios[1])
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sc.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sc.Prepare(ctx); err != nil {
		t.Fatal(err)
	}

	w := sc.NewWorker(0)
	defer w.Close()
	res := w.RunOnce(ctx)
	if res.Err != nil {
		t.Fatalf("push after mount misses failed: %v", res.Err)
	}
	// Two layers and the config: three misses, each finished with an upload.
	if got := res.Counters[dedup.CounterMountMisses]; got != 3 {
		t.Errorf("mount misses = %d, want 3", got)
	}
	if res.Bytes == 0 || len(res.BlobSteps) != 3 {
		t.Errorf("bytes=%d steps=%d", res.Bytes, len(res.BlobSteps))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
