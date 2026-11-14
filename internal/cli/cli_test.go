// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

const testNamespace = "bench"

// testEnv runs orbctl commands against a fake cluster.
type testEnv struct {
	t      *testing.T
	client client.Client
	detail []byte
	out    bytes.Buffer
	errOut bytes.Buffer
}

func newTestEnv(t *testing.T, funcs *interceptor.Funcs, objects ...client.Object) *testEnv {
	t.Helper()
	scheme, err := newScheme()
	if err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...)
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	return &testEnv{t: t, client: builder.Build()}
}

// run executes orbctl with args and returns its error.
func (e *testEnv) run(args ...string) error {
	e.out.Reset()
	e.errOut.Reset()
	o := &options{
		out:          &e.out,
		errOut:       &e.errOut,
		pollInterval: 10 * time.Millisecond,
		connect: func(*options) (*kube, error) {
			return &kube{
				client:    e.client,
				namespace: testNamespace,
				fetchDetail: func(context.Context, string, bool) ([]byte, error) {
					return e.detail, nil
				},
			}, nil
		},
	}
	cmd := newRootCommand(o)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func TestParseOverrides(t *testing.T) {
	o, err := parseOverrides([]string{
		"cleanup=Keep", "seed=7", "repositoryPrefix=other", "workersPerAgent=25",
		"stages.load=30s:10,2m:50,30s:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if *o.Cleanup != orbv1.CleanupKeep || *o.Seed != 7 || *o.RepositoryPrefix != "other" || *o.WorkersPerAgent != 25 {
		t.Errorf("unexpected overrides: %+v", o)
	}
	stages := o.Stages["load"]
	if len(stages) != 3 || stages[1].Duration.Duration != 2*time.Minute || stages[1].Workers != 50 {
		t.Errorf("unexpected stages: %+v", stages)
	}

	none, err := parseOverrides(nil)
	if err != nil || none != nil {
		t.Errorf("no --set flags should give nil overrides, got %+v, %v", none, err)
	}

	for _, bad := range []string{"color=blue", "cleanup=Sometimes", "seed=abc", "workersPerAgent=0", "stages.=1s:1", "novalue"} {
		if _, err := parseOverrides([]string{bad}); err == nil {
			t.Errorf("--set %s was accepted", bad)
		}
	}
	if _, err := parseOverrides([]string{"color=blue"}); !strings.Contains(err.Error(), "Supported --set keys") {
		t.Errorf("unknown key error does not list the supported keys: %v", err)
	}
}

func TestParseStagesRejectsBadEntries(t *testing.T) {
	for _, bad := range []string{"", "30s", "x:10", "30s:-1", "0s:5", "30s:ten"} {
		if _, err := parseStages(bad); err == nil {
			t.Errorf("stages %q were accepted", bad)
		}
	}
}

func TestExamplesAreValid(t *testing.T) {
	files, err := filepath.Glob("../../examples/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples found: %v", err)
	}
	benchmarks := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		decoded, _, err := decodeBenchmarks(data)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		for _, b := range decoded {
			spec := b.Spec.DeepCopy()
			spec.Default()
			if err := spec.Validate(); err != nil {
				t.Errorf("%s: %s is not valid: %v", file, b.Name, err)
			}
			benchmarks++
		}
	}
	if benchmarks < 3 {
		t.Errorf("found %d example benchmarks, want at least 3", benchmarks)
	}
}

func testBenchmark() *orbv1.OrbBenchmark {
	return &orbv1.OrbBenchmark{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "e2e"},
		Spec: orbv1.OrbBenchmarkSpec{
			Registry:  orbv1.RegistrySpec{URL: "http://registry:5000", RepositoryPrefix: "bench"},
			ImageSet:  orbv1.ImageSetSpec{Type: orbv1.ImageSetRandom},
			Scenarios: []orbv1.ScenarioSpec{{Name: "load", Type: orbv1.ScenarioLoad}},
		},
	}
}

func TestBenchmarkApplyCreatesThenUpdates(t *testing.T) {
	env := newTestEnv(t, nil)
	path := filepath.Join(t.TempDir(), "b.yaml")
	data, _ := os.ReadFile("../../examples/harbor-load.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := env.run("benchmark", "apply", "-f", path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.out.String(), "orbbenchmark/harbor-load created") {
		t.Errorf("unexpected output %q", env.out.String())
	}
	if err := env.run("benchmark", "apply", "-f", path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.out.String(), "configured") {
		t.Errorf("second apply should update, got %q", env.out.String())
	}
}

func TestRunStartPrintsOnlyTheName(t *testing.T) {
	env := newTestEnv(t, nil, testBenchmark())
	if err := env.run("run", "start", "e2e", "-o", "name", "--set", "cleanup=Keep"); err != nil {
		t.Fatal(err)
	}
	name := strings.TrimSpace(env.out.String())
	if !strings.HasPrefix(name, "e2e-") || strings.Contains(name, " ") {
		t.Fatalf("expected only the run name, got %q", env.out.String())
	}

	var run orbv1.OrbBenchmarkRun
	if err := env.client.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, &run); err != nil {
		t.Fatal(err)
	}
	if run.Spec.BenchmarkRef != "e2e" || *run.Spec.Overrides.Cleanup != orbv1.CleanupKeep {
		t.Errorf("unexpected run spec: %+v", run.Spec)
	}
}

func TestRunStartFailsForMissingBenchmark(t *testing.T) {
	env := newTestEnv(t, nil)
	if err := env.run("run", "start", "nope"); err == nil {
		t.Fatal("starting a run of a missing benchmark should fail")
	}
}

func finishedRun(name string, phase orbv1.RunPhase) *orbv1.OrbBenchmarkRun {
	return &orbv1.OrbBenchmarkRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: name},
		Spec:       orbv1.OrbBenchmarkRunSpec{BenchmarkRef: "e2e"},
		Status:     orbv1.OrbBenchmarkRunStatus{Phase: phase, Message: "boom", ResultName: name},
	}
}

func TestRunWatchExitStatus(t *testing.T) {
	env := newTestEnv(t, nil,
		finishedRun("ok", orbv1.PhaseSucceeded),
		finishedRun("failed", orbv1.PhaseFailed),
		finishedRun("aborted", orbv1.PhaseAborted))

	if err := env.run("run", "watch", "ok"); err != nil {
		t.Errorf("watching a succeeded run returned %v", err)
	}
	if err := env.run("run", "watch", "failed"); err == nil {
		t.Error("watching a failed run should return an error")
	}
	if err := env.run("run", "watch", "aborted"); err == nil {
		t.Error("watching an aborted run should return an error")
	}
}

func TestRunAbortPatchesSpec(t *testing.T) {
	running := finishedRun("r1", orbv1.PhaseRunning)
	env := newTestEnv(t, nil, running)
	if err := env.run("run", "abort", "r1"); err != nil {
		t.Fatal(err)
	}
	var run orbv1.OrbBenchmarkRun
	_ = env.client.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "r1"}, &run)
	if !run.Spec.Abort {
		t.Error("spec.abort was not set")
	}
}

func testResult() *orbv1.OrbBenchmarkResult {
	return &orbv1.OrbBenchmarkResult{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "ok"},
		Summary: orbv1.ResultSummary{
			RunName: "ok", RunUID: "uid-1", BenchmarkName: "e2e", Agents: 2,
			Registry: orbv1.RegistryInfo{URL: "http://registry:5000", Module: "generic"},
			Detail:   orbv1.DetailReference{Store: "sqlite", RunUID: "uid-1"},
			Scenarios: []orbv1.ScenarioResult{{
				Name: "load", Type: orbv1.ScenarioLoad, DurationSeconds: 60,
				Operations: []orbv1.OperationResult{{
					Operation: "pull", Count: 600, Errors: 2, OpsPerSecond: 10,
					Latency:    orbv1.LatencySummary{P50Millis: 12.5, P90Millis: 20, P99Millis: 40},
					Throughput: &orbv1.ThroughputSummary{TotalBytes: 6e9, AggregateBytesPerSecond: 1e8},
				}},
			}},
		},
	}
}

func testDetail() protocol.Detail {
	return protocol.Detail{RunUID: "uid-1", Scenarios: []protocol.ScenarioDetail{{
		Name: "load",
		Intervals: []protocol.IntervalDetail{{Interval: 0, Workers: 10, Operations: []protocol.IntervalOperation{
			{Operation: "pull", Count: 10, Bytes: 1000, P50Millis: 12},
		}}},
		Phases: []protocol.PhaseDetail{{Operation: "pull", Metric: "phase:dns", Count: 10}},
		Agents: []protocol.AgentInterval{{Agent: 0, Interval: 0, Workers: 5, CPUUtilization: 0.3}},
		Errors: []protocol.ErrorDetail{{Operation: "pull", Type: "http_503", Count: 2, Example: "unavailable, try later"}},
	}}}
}

func TestResultGetTableAndJSON(t *testing.T) {
	env := newTestEnv(t, nil, finishedRun("ok", orbv1.PhaseSucceeded), testResult())

	if err := env.run("result", "get", "ok"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"P99 MS", "pull", "12.50", "100.0", "Saturated:   no"} {
		if !strings.Contains(env.out.String(), want) {
			t.Errorf("table output lacks %q:\n%s", want, env.out.String())
		}
	}

	if err := env.run("result", "get", "ok", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var decoded orbv1.OrbBenchmarkResult
	if err := json.Unmarshal(env.out.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON output does not decode: %v", err)
	}
	if decoded.Summary.RunName != "ok" || decoded.Name != "ok" {
		t.Errorf("unexpected JSON result: %+v", decoded)
	}
}

func TestResultExportJSONAndCSV(t *testing.T) {
	env := newTestEnv(t, nil, finishedRun("ok", orbv1.PhaseSucceeded), testResult())
	env.detail, _ = json.Marshal(testDetail())
	dir := t.TempDir()

	jsonPath := filepath.Join(dir, "export.json")
	if err := env.run("result", "export", "ok", "-o", "json", "--output", jsonPath); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	data, _ := os.ReadFile(jsonPath)
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["summary"]; !ok {
		t.Error("export has no summary")
	}
	if _, ok := doc["detail"]; !ok {
		t.Error("export has no detail")
	}

	csvDir := filepath.Join(dir, "csv")
	if err := env.run("result", "export", "ok", "-o", "csv", "--output", csvDir); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"summary_operations.csv", "intervals.csv", "phases.csv", "agents.csv", "errors.csv"} {
		content, err := os.ReadFile(filepath.Join(csvDir, file))
		if err != nil {
			t.Errorf("missing %s: %v", file, err)
			continue
		}
		if lines := strings.Count(string(content), "\n"); lines < 2 {
			t.Errorf("%s has %d lines, want a header and data", file, lines)
		}
	}
	if _, err := os.Stat(filepath.Join(csvDir, "samples.csv")); err == nil {
		t.Error("samples.csv was written although no samples were recorded")
	}
}

func TestBuildCSVTablesQuotesExampleMessages(t *testing.T) {
	tables := buildCSVTables(testResult().Summary, testDetail())
	var errorsTable csvTable
	for _, table := range tables {
		if table.file == "errors.csv" {
			errorsTable = table
		}
	}
	if len(errorsTable.rows) != 2 || errorsTable.rows[1][4] != "unavailable, try later" {
		t.Errorf("unexpected errors table: %v", errorsTable.rows)
	}
}

func TestResultCompare(t *testing.T) {
	a := testResult()
	b := testResult()
	b.Name, b.Summary.RunName = "other", "other"
	b.Summary.Scenarios[0].Operations[0].Latency.P50Millis = 25
	env := newTestEnv(t, nil, a, b)

	if err := env.run("result", "compare", "ok", "other"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.out.String(), "+100.0%") {
		t.Errorf("comparison lacks the p50 change:\n%s", env.out.String())
	}
}

func TestRegistryCheck(t *testing.T) {
	// The interceptor plays the controller: a created check run finishes at once.
	funcs := &interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if run, ok := obj.(*orbv1.OrbBenchmarkRun); ok && run.Spec.CheckOnly {
				run.Status.Phase = orbv1.PhaseSucceeded
				run.Status.Checks = []orbv1.CheckResult{{Name: "ping", Passed: true}, {Name: "push", Passed: true}}
			}
			return c.Create(ctx, obj, opts...)
		},
	}
	env := newTestEnv(t, funcs, testBenchmark())

	if err := env.run("registry", "check", "e2e"); err != nil {
		t.Fatalf("check failed: %v\n%s", err, env.out.String())
	}
	if !strings.Contains(env.out.String(), "All registry checks passed") {
		t.Errorf("unexpected output:\n%s", env.out.String())
	}

	var runs orbv1.OrbBenchmarkRunList
	_ = env.client.List(context.Background(), &runs)
	if len(runs.Items) != 0 {
		t.Errorf("the check run was not deleted: %d runs left", len(runs.Items))
	}
}

func TestRegistryCheckReportsFailures(t *testing.T) {
	funcs := &interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if run, ok := obj.(*orbv1.OrbBenchmarkRun); ok {
				run.Status.Phase = orbv1.PhaseFailed
				run.Status.Message = "a registry check failed"
				run.Status.Checks = []orbv1.CheckResult{{Name: "push", Passed: false, Message: "403 Forbidden"}}
			}
			return c.Create(ctx, obj, opts...)
		},
	}
	env := newTestEnv(t, funcs, testBenchmark())

	if err := env.run("registry", "check", "e2e"); err == nil {
		t.Fatal("a failed check should return an error")
	}
	if !strings.Contains(env.out.String(), "FAILED") {
		t.Errorf("failed check not shown:\n%s", env.out.String())
	}
}
