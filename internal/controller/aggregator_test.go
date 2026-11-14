// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

// loadPlan is a plan with one load scenario and a 2-stage profile.
func loadPlan(agents int) protocol.Plan {
	spec := orbv1.OrbBenchmarkSpec{
		Registry: orbv1.RegistrySpec{URL: "https://harbor.example.com", Module: "harbor", RepositoryPrefix: "bench"},
		ImageSet: orbv1.ImageSetSpec{Type: orbv1.ImageSetRandom},
		Scenarios: []orbv1.ScenarioSpec{{
			Name: "load", Type: orbv1.ScenarioLoad,
			Load: &orbv1.LoadScenario{Mix: map[string]int{"pull": 100}},
			Profile: orbv1.LoadProfile{
				Warmup: &metav1.Duration{Duration: time.Second},
				Stages: []orbv1.Stage{
					{Duration: metav1.Duration{Duration: 5 * time.Second}, Workers: 10},
					{Duration: metav1.Duration{Duration: 5 * time.Second}, Workers: 10},
				},
			},
		}},
	}
	spec.Default()
	return protocol.Plan{RunUID: "uid", RunName: "run", Namespace: "default", Agents: agents, Spec: spec}
}

func pullReport(t *testing.T, interval, stage int, warmup bool, latency time.Duration, cpu float64) protocol.IntervalReport {
	t.Helper()
	rec := metrics.NewRecorder(false, time.Now())
	for i := 0; i < 10; i++ {
		rec.Record(metrics.Result{Operation: "pull", Latency: latency, Total: latency, Bytes: 1000, TransferTime: time.Millisecond,
			Phases: map[string]time.Duration{"manifest": time.Millisecond}})
	}
	rec.Record(metrics.Result{Operation: "pull", Err: errors.New("x"), ErrorType: "timeout"})
	ops, _, err := rec.Flush()
	if err != nil {
		t.Fatal(err)
	}
	return protocol.IntervalReport{
		Scenario: 0, ScenarioName: "load", Interval: interval, Stage: stage, Warmup: warmup,
		Workers: 5, CPUUtilization: cpu, Operations: ops,
	}
}

func TestAggregatorMergesAndSummarizes(t *testing.T) {
	a := NewAggregator(nil, loadPlan(2))
	for agent := 0; agent < 2; agent++ {
		if err := a.AddReport(agent, pullReport(t, 0, 0, true, time.Second, 0.1)); err != nil {
			t.Fatal(err)
		}
		for interval := 1; interval <= 4; interval++ {
			if err := a.AddReport(agent, pullReport(t, interval, interval/3, false, 10*time.Millisecond, 0.1)); err != nil {
				t.Fatal(err)
			}
		}
	}

	results := a.ScenarioResults()
	op := results[0].Operations[0]
	if op.Count != 88 || op.Errors != 8 || op.ErrorsByType["timeout"] != 8 {
		t.Errorf("unexpected counts %+v", op)
	}
	if op.Latency.P99Millis > 10.1 {
		t.Errorf("warm-up leaked into measured latency: p99 %v", op.Latency.P99Millis)
	}
	if results[0].DurationSeconds != 4 || op.OpsPerSecond != 22 {
		t.Errorf("duration %v, ops/s %v", results[0].DurationSeconds, op.OpsPerSecond)
	}
	if op.Throughput == nil || op.Throughput.TotalBytes != 80_000 {
		t.Errorf("unexpected throughput %+v", op.Throughput)
	}
	if _, ok := op.Phases["manifest"]; !ok {
		t.Error("phase summary missing")
	}
	if len(results[0].Warmup) != 1 || results[0].Warmup[0].Latency.P50Millis < 999 {
		t.Errorf("warm-up not kept separately: %+v", results[0].Warmup)
	}
	if len(results[0].Stages) != 2 {
		t.Errorf("got %d stages, want 2", len(results[0].Stages))
	}

	live := a.Live(0)
	if live == nil || live.Workers != 10 || live.Operations[0].OpsPerSecond != 22 {
		t.Errorf("unexpected live stats %+v", live)
	}
	if saturated, _ := a.Saturation(); saturated {
		t.Error("low CPU flagged as saturated")
	}
}

func TestSaturation(t *testing.T) {
	a := NewAggregator(nil, loadPlan(1))
	for interval := 1; interval <= 10; interval++ {
		cpu := 0.2
		if interval <= 2 { // 20% of intervals above the threshold
			cpu = 0.95
		}
		if err := a.AddReport(0, pullReport(t, interval, 0, false, time.Millisecond, cpu)); err != nil {
			t.Fatal(err)
		}
	}
	saturated, reason := a.Saturation()
	if !saturated || !strings.Contains(reason, "20%") {
		t.Errorf("saturated=%v reason=%q", saturated, reason)
	}
}

func TestResultFitsSizeLimit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	plan := loadPlan(1)
	plan.Spec.Scenarios[0].Profile.Stages = []orbv1.Stage{{Duration: metav1.Duration{Duration: time.Hour}, Workers: 10}}
	a := NewAggregator(st, plan)
	_ = st.EnsureRun("uid", "default", "run")

	// Many operation types per interval make each time series point large.
	rec := metrics.NewRecorder(false, time.Now())
	for i := 0; i < 400; i++ {
		rec.Record(metrics.Result{Operation: "op-" + strings.Repeat("x", 40) + string(rune('A'+i%60)), Latency: time.Millisecond, Total: time.Millisecond})
	}
	ops, _, _ := rec.Flush()
	for interval := 0; interval < 240; interval++ {
		r := protocol.IntervalReport{Scenario: 0, ScenarioName: "load", Interval: interval, Workers: 1, Operations: ops}
		if err := a.AddReport(0, r); err != nil {
			t.Fatal(err)
		}
	}

	run := &orbv1.OrbBenchmarkRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default", UID: types.UID("uid")}}
	result, err := buildResult(resultInput{Run: run, Spec: &plan.Spec, Aggregator: a, Store: st, Agents: 1, CompletedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if len(data) > resultSizeLimit {
		t.Fatalf("result is %d bytes, limit %d", len(data), resultSizeLimit)
	}
	series := result.Summary.Scenarios[0].TimeSeries
	if series != nil && len(series.Points) > maxTimePoints {
		t.Errorf("time series has %d points, more than %d", len(series.Points), maxTimePoints)
	}
}

func TestBuildAgentJob(t *testing.T) {
	plan := loadPlan(3)
	spec := plan.Spec
	spec.Registry.CredentialsSecret = "harbor-creds"
	spec.Registry.CASecret = "harbor-ca"
	spec.ImageSet = orbv1.ImageSetSpec{Type: orbv1.ImageSetStatic, Static: &orbv1.StaticImageSet{
		Images: []string{"docker.io/library/alpine:3"}, SourceCredentialsSecret: "hub", StorageMedium: "Memory",
		StorageLimit: ptrTo(resource.MustParse("1Gi")),
	}}
	run := &orbv1.OrbBenchmarkRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "bench", UID: types.UID("uid-1")},
		Spec:       orbv1.OrbBenchmarkRunSpec{BenchmarkRef: "b"},
	}

	job := buildAgentJob(run, &spec, 3, jobSettings{Image: "orb:test", AgentAPIURL: "http://orb-controller.orb-system.svc:8082"})
	if job.Name != "r1-agents" || *job.Spec.Completions != 3 || *job.Spec.Parallelism != 3 || *job.Spec.BackoffLimit != 0 {
		t.Errorf("unexpected job spec %+v", job.Spec)
	}
	pod := job.Spec.Template.Spec
	env := map[string]string{}
	for _, e := range pod.Containers[0].Env {
		env[e.Name] = e.Value
		if e.Name == EnvAgentToken && (e.ValueFrom == nil || e.ValueFrom.SecretKeyRef.Name != "r1-agent-token") {
			t.Error("token is not read from the token secret")
		}
	}
	if env[EnvRunUID] != "uid-1" || env[EnvControllerURL] == "" || env[EnvRunName] != "r1" {
		t.Errorf("unexpected env %v", env)
	}
	mounts := map[string]bool{}
	for _, m := range pod.Containers[0].VolumeMounts {
		mounts[m.MountPath] = true
	}
	for _, path := range []string{MountRegistryCredentials, MountSourceCredentials, MountCA, MountScratch, MountTmp} {
		if !mounts[path] {
			t.Errorf("missing mount %s", path)
		}
	}
	for _, v := range pod.Volumes {
		if v.Name == "scratch" && (v.EmptyDir.Medium != "Memory" || v.EmptyDir.SizeLimit.String() != "1Gi") {
			t.Errorf("scratch volume %+v", v.EmptyDir)
		}
	}
	if len(pod.TopologySpreadConstraints) != 1 {
		t.Error("agents are not spread across nodes")
	}
	if !*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem || !*pod.SecurityContext.RunAsNonRoot {
		t.Error("agent must run as non-root with a read-only root file system")
	}
}

func TestAgentAPI(t *testing.T) {
	coordinator := NewCoordinator()
	plan := loadPlan(1)
	rs := coordinator.Start(plan, "secret", NewAggregator(nil, plan))
	server := httptest.NewServer(newAPIHandler(coordinator, nil, logr.Discard()))
	defer server.Close()

	call := func(method, path, token string, body any) *http.Response {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequest(method, server.URL+path, &buf)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	wrong := call("GET", protocol.AgentPath(protocol.PathPlan, "uid", 0), "wrong", nil)
	_ = wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d", wrong.StatusCode)
	}
	resp := call("GET", protocol.AgentPath(protocol.PathPlan, "uid", 0), "secret", nil)
	defer resp.Body.Close()
	var got protocol.Plan
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.RunName != "run" {
		t.Errorf("plan %+v", got)
	}

	if resp := call("POST", protocol.AgentPath(protocol.PathState, "uid", 0), "secret", protocol.StateUpdate{State: protocol.StateReady}); resp.StatusCode != http.StatusNoContent {
		t.Errorf("state update: status %d", resp.StatusCode)
	}
	if rs.Snapshot().Phase != orbv1.PhaseSeeding {
		t.Errorf("phase %s after ready", rs.Snapshot().Phase)
	}
	resp = call("GET", protocol.AgentPath(protocol.PathCommand, "uid", 0), "secret", nil)
	var cmd protocol.Command
	_ = json.NewDecoder(resp.Body).Decode(&cmd)
	if cmd.Action != protocol.ActionSeed {
		t.Errorf("command %+v", cmd)
	}

	if resp := call("POST", protocol.AgentPath(protocol.PathReport, "uid", 0), "secret", pullReport(t, 1, 0, false, time.Millisecond, 0.1)); resp.StatusCode != http.StatusNoContent {
		t.Errorf("report: status %d", resp.StatusCode)
	}
	if !rs.Aggregator().HasData() {
		t.Error("report did not reach the aggregator")
	}
	if resp := call("GET", protocol.AgentPath(protocol.PathCommand, "uid", 7), "secret", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad agent index: status %d", resp.StatusCode)
	}
	if resp := call("GET", protocol.AgentPath(protocol.PathCommand, "nope", 0), "secret", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown run: status %d", resp.StatusCode)
	}
}
