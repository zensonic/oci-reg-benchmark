// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

const (
	// finalizer makes sure a run's SQLite data is deleted with the run (RES-12).
	finalizer = "orb.kubectl.dk/detail-data"

	// activeRequeue is how often an active run's status is refreshed.
	activeRequeue = 2 * time.Second

	// preparingTimeout fails runs whose agents never become ready, for
	// example because their image cannot be pulled.
	preparingTimeout = 30 * time.Minute
)

// RunReconciler drives OrbBenchmarkRun objects through their phases (ARCH-4).
type RunReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Coordinator *Coordinator
	Store       *store.Store
	Jobs        jobSettings
}

// +kubebuilder:rbac:groups=orb.kubectl.dk,resources=orbbenchmarks,verbs=get;list;watch
// +kubebuilder:rbac:groups=orb.kubectl.dk,resources=orbbenchmarkruns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=orb.kubectl.dk,resources=orbbenchmarkruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=orb.kubectl.dk,resources=orbbenchmarkruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=orb.kubectl.dk,resources=orbbenchmarkresults,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager registers the reconciler. Changes to owned Jobs trigger
// a reconcile, so agent pod failures are noticed quickly.
func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&orbv1.OrbBenchmarkRun{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

// Reconcile moves one run forward.
func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run orbv1.OrbBenchmarkRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !run.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.delete(ctx, &run)
	}
	if !controllerutil.ContainsFinalizer(&run, finalizer) {
		controllerutil.AddFinalizer(&run, finalizer)
		return ctrl.Result{RequeueAfter: time.Millisecond}, r.Update(ctx, &run)
	}

	switch {
	case run.Status.Phase == "" || run.Status.Phase == orbv1.PhasePending:
		return r.start(ctx, &run)
	case run.Status.Phase.IsFinished():
		return ctrl.Result{}, nil
	default:
		return r.follow(ctx, &run)
	}
}

// delete removes a run's detail data and forgets it (RES-12).
func (r *RunReconciler) delete(ctx context.Context, run *orbv1.OrbBenchmarkRun) error {
	if !controllerutil.ContainsFinalizer(run, finalizer) {
		return nil
	}
	if rs, ok := r.Coordinator.Get(string(run.UID)); ok {
		rs.RequestAbort()
	}
	r.Coordinator.Remove(string(run.UID))
	forgetRunMetrics(run.Namespace, run.Name)
	if err := r.Store.DeleteRun(string(run.UID)); err != nil {
		return fmt.Errorf("deleting detail data: %w", err)
	}
	controllerutil.RemoveFinalizer(run, finalizer)
	return r.Update(ctx, run)
}

// start takes a Pending run: snapshot the spec, create the token Secret and
// the agent Job, and register the run with the coordinator.
func (r *RunReconciler) start(ctx context.Context, run *orbv1.OrbBenchmarkRun) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var benchmark orbv1.OrbBenchmark
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.BenchmarkRef}, &benchmark)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, r.setFailed(ctx, run, fmt.Sprintf("OrbBenchmark %q not found", run.Spec.BenchmarkRef))
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	// RUN-3, RUN-4: overrides are applied to a copy, which becomes the snapshot.
	spec, err := benchmark.Spec.ApplyOverrides(run.Spec.Overrides)
	if err != nil {
		return ctrl.Result{}, r.setFailed(ctx, run, err.Error())
	}
	spec.Default()
	if err := spec.Validate(); err != nil {
		return ctrl.Result{}, r.setFailed(ctx, run, "invalid benchmark: "+err.Error())
	}
	agents := spec.AgentCount()

	token, err := r.ensureTokenSecret(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	job := buildAgentJob(run, spec, agents, r.Jobs)
	if err := controllerutil.SetControllerReference(run, job, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("creating agent job: %w", err)
	}

	plan := protocol.Plan{
		RunName:        run.Name,
		RunUID:         string(run.UID),
		Namespace:      run.Namespace,
		BenchmarkName:  benchmark.Name,
		Spec:           *spec,
		Agents:         agents,
		RepositoryRoot: spec.Registry.RepositoryPrefix + "/" + run.Name,
		CheckOnly:      run.Spec.CheckOnly,
	}
	if err := r.Store.EnsureRun(plan.RunUID, run.Namespace, run.Name); err != nil {
		return ctrl.Result{}, err
	}
	r.Coordinator.Start(plan, token, NewAggregator(r.Store, plan))

	now := metav1.Now()
	run.Status.Phase = orbv1.PhasePreparing
	run.Status.Message = fmt.Sprintf("starting %d agents", agents)
	run.Status.Agents = agents
	run.Status.StartedAt = &now
	run.Status.BenchmarkSpec = spec
	logger.Info("run started", "run", run.Name, "agents", agents)
	return ctrl.Result{RequeueAfter: activeRequeue}, r.Status().Update(ctx, run)
}

// ensureTokenSecret creates the agent token Secret, or reads the token from
// an existing one (when a previous reconcile created it but failed later).
func (r *RunReconciler) ensureTokenSecret(ctx context.Context, run *orbv1.OrbBenchmarkRun) (string, error) {
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: tokenSecretName(run)}, &existing)
	if err == nil {
		return string(existing.Data[tokenSecretKey]), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}

	token, err := newToken()
	if err != nil {
		return "", err
	}
	secret := buildTokenSecret(run, token)
	if err := controllerutil.SetControllerReference(run, secret, r.Scheme); err != nil {
		return "", err
	}
	if err := r.Create(ctx, secret); err != nil {
		return "", fmt.Errorf("creating agent token secret: %w", err)
	}
	return token, nil
}

// follow mirrors the coordinator's state of an active run into its status,
// writes the result when it is time, and handles aborts and failures.
func (r *RunReconciler) follow(ctx context.Context, run *orbv1.OrbBenchmarkRun) (ctrl.Result, error) {
	rs, ok := r.Coordinator.Get(string(run.UID))
	if !ok {
		return ctrl.Result{}, r.setFailed(ctx, run, "the controller restarted during the run, so its coordination state was lost")
	}

	if run.Spec.Abort {
		rs.RequestAbort()
	}
	if err := r.checkJob(ctx, run, rs); err != nil {
		return ctrl.Result{}, err
	}
	if run.Status.Phase == orbv1.PhasePreparing && run.Status.StartedAt != nil &&
		time.Since(run.Status.StartedAt.Time) > preparingTimeout {
		rs.Fail(fmt.Sprintf("agents were not ready after %v; check the agent pods", preparingTimeout))
	}

	snap := rs.Snapshot()
	needsResult := snap.Phase == orbv1.PhaseCollecting ||
		(snap.Phase == orbv1.PhaseFailed && rs.Aggregator() != nil && rs.Aggregator().HasData())
	if !snap.Collected && needsResult {
		if err := r.writeResult(ctx, run, rs, snap); err != nil {
			return ctrl.Result{}, err
		}
		rs.MarkCollected()
		snap = rs.Snapshot()
	}

	r.mirrorStatus(run, rs, snap)
	if err := r.Status().Update(ctx, run); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if snap.Phase.IsFinished() {
		if err := r.recordCleanup(ctx, run, snap); err != nil {
			return ctrl.Result{}, err
		}
		forgetRunMetrics(run.Namespace, run.Name)
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: activeRequeue}, nil
}

// checkJob fails the run if an agent pod failed.
func (r *RunReconciler) checkJob(ctx context.Context, run *orbv1.OrbBenchmarkRun, rs *RunState) error {
	var job batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: agentJobName(run)}, &job)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.Status.Failed > 0 {
		rs.Fail(fmt.Sprintf("an agent pod failed; see: kubectl logs -n %s -l %s=%s", run.Namespace, LabelRun, run.Name))
	}
	return nil
}

// writeResult creates or replaces the run's OrbBenchmarkResult (RES-1).
func (r *RunReconciler) writeResult(ctx context.Context, run *orbv1.OrbBenchmarkRun, rs *RunState, snap Snapshot) error {
	spec := run.Status.BenchmarkSpec
	if spec == nil {
		spec = &rs.Plan.Spec
	}
	result, err := buildResult(resultInput{
		Run:         run,
		Spec:        spec,
		Snapshot:    snap,
		Aggregator:  rs.Aggregator(),
		Store:       r.Store,
		Agents:      rs.Plan.Agents,
		CompletedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("building result: %w", err)
	}
	if err := controllerutil.SetControllerReference(run, result, r.Scheme); err != nil {
		return err
	}

	var existing orbv1.OrbBenchmarkResult
	err = r.Get(ctx, client.ObjectKeyFromObject(result), &existing)
	switch {
	case apierrors.IsNotFound(err):
		err = r.Create(ctx, result)
	case err == nil:
		existing.Summary = result.Summary
		existing.Labels = result.Labels
		err = r.Update(ctx, &existing)
	}
	if err != nil {
		return fmt.Errorf("writing result: %w", err)
	}
	run.Status.ResultName = result.Name
	return nil
}

// mirrorStatus copies the coordinator's snapshot into the run's status.
func (r *RunReconciler) mirrorStatus(run *orbv1.OrbBenchmarkRun, rs *RunState, snap Snapshot) {
	status := &run.Status
	status.Phase = snap.Phase
	status.AgentsReady = snap.AgentsReady
	status.Checks = snap.Checks
	status.ResolvedImages = snap.ResolvedImages
	status.RegistryVersion = snap.RegistryVersion
	status.Cleanup = snap.Cleanup
	if snap.Message != "" {
		status.Message = snap.Message
	} else {
		status.Message = phaseMessage(snap, rs)
	}

	status.ActiveScenario = ""
	if snap.Phase == orbv1.PhaseRunning && snap.Scenario < len(rs.Plan.Spec.Scenarios) {
		status.ActiveScenario = rs.Plan.Spec.Scenarios[snap.Scenario].Name
		if aggregator := rs.Aggregator(); aggregator != nil {
			if live := aggregator.Live(snap.Scenario); live != nil {
				status.Live = live
			}
		}
	}

	if snap.Phase.IsFinished() && status.CompletedAt == nil {
		now := metav1.Now()
		status.CompletedAt = &now
	}
}

func phaseMessage(snap Snapshot, rs *RunState) string {
	switch snap.Phase {
	case orbv1.PhasePreparing:
		return fmt.Sprintf("%d of %d agents ready", snap.AgentsReady, rs.Plan.Agents)
	case orbv1.PhaseSeeding:
		return "pushing seed content"
	case orbv1.PhaseRunning:
		return fmt.Sprintf("running scenario %d of %d", snap.Scenario+1, len(rs.Plan.Spec.Scenarios))
	case orbv1.PhaseCollecting:
		return "writing results"
	case orbv1.PhaseCleaningUp:
		return "deleting pushed artifacts"
	case orbv1.PhaseSucceeded:
		return "finished"
	case orbv1.PhaseAborted:
		return "aborted"
	default:
		return ""
	}
}

// setFailed marks a run that never started as failed.
func (r *RunReconciler) setFailed(ctx context.Context, run *orbv1.OrbBenchmarkRun, message string) error {
	if rs, ok := r.Coordinator.Get(string(run.UID)); ok {
		rs.Fail(message)
	}
	now := metav1.Now()
	run.Status.Phase = orbv1.PhaseFailed
	run.Status.Message = message
	run.Status.CompletedAt = &now
	return r.Status().Update(ctx, run)
}

// recordCleanup copies the cleanup message into the result. The result is
// written in the Collecting phase, before cleanup runs, so the message is
// added once cleanup has finished (CLEAN-4, RES-6).
func (r *RunReconciler) recordCleanup(ctx context.Context, run *orbv1.OrbBenchmarkRun, snap Snapshot) error {
	if snap.Cleanup == "" || run.Status.ResultName == "" {
		return nil
	}
	var result orbv1.OrbBenchmarkResult
	key := client.ObjectKey{Namespace: run.Namespace, Name: run.Status.ResultName}
	if err := r.Get(ctx, key, &result); err != nil {
		return client.IgnoreNotFound(err)
	}
	if result.Summary.Cleanup == snap.Cleanup {
		return nil
	}
	result.Summary.Cleanup = snap.Cleanup
	if err := r.Update(ctx, &result); err != nil {
		return fmt.Errorf("recording cleanup in result: %w", err)
	}
	return nil
}
