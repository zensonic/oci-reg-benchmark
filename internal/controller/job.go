// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

// Labels set on objects the controller creates.
const (
	LabelRun       = "orb.kubectl.dk/run"
	LabelBenchmark = "orb.kubectl.dk/benchmark"
	LabelComponent = "app.kubernetes.io/component"
)

// Agent pod contract (docs/architecture.md, "Agent pod contract").
const (
	EnvControllerURL = "ORB_CONTROLLER_URL"
	EnvRunUID        = "ORB_RUN_UID"
	EnvRunName       = "ORB_RUN_NAME"
	EnvAgentToken    = "ORB_AGENT_TOKEN"

	MountRegistryCredentials = "/etc/orb/registry"
	MountSourceCredentials   = "/etc/orb/source"
	MountCA                  = "/etc/orb/ca"
	MountScratch             = "/scratch"
	MountTmp                 = "/tmp"

	tokenSecretKey = "token"
)

// defaultScratchSize is the scratch volume limit when no static image set
// is used.
var defaultScratchSize = resource.MustParse("256Mi")

// agentJobName is the name of a run's agent Job.
func agentJobName(run *orbv1.OrbBenchmarkRun) string {
	return run.Name + "-agents"
}

// tokenSecretName is the name of a run's agent token Secret.
func tokenSecretName(run *orbv1.OrbBenchmarkRun) string {
	return run.Name + "-agent-token"
}

// newToken returns a random agent token.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating agent token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// buildTokenSecret builds the Secret that holds the agents' token.
func buildTokenSecret(run *orbv1.OrbBenchmarkRun, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tokenSecretName(run),
			Namespace: run.Namespace,
			Labels:    runLabels(run),
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{tokenSecretKey: token},
	}
}

// jobSettings are the controller-wide settings for agent Jobs.
type jobSettings struct {
	Image       string
	AgentAPIURL string
}

// buildAgentJob builds the Indexed Job that runs a run's agents (ARCH-5).
func buildAgentJob(run *orbv1.OrbBenchmarkRun, spec *orbv1.OrbBenchmarkSpec, agents int, settings jobSettings) *batchv1.Job {
	labels := runLabels(run)
	labels[LabelComponent] = "agent"

	completions := int32(agents)
	backoffLimit := int32(0)
	ttl := int32(3600)
	indexed := batchv1.IndexedCompletion

	podSpec := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptrTo(false),
		NodeSelector:                 spec.Agents.NodeSelector,
		Tolerations:                  spec.Agents.Tolerations,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptrTo(true),
			RunAsUser:      ptrTo(int64(65532)),
			RunAsGroup:     ptrTo(int64(65532)),
			FSGroup:        ptrTo(int64(65532)),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:      "agent",
			Image:     settings.Image,
			Args:      []string{"agent"},
			Env:       agentEnv(run, settings),
			Resources: spec.Agents.Resources,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptrTo(false),
				ReadOnlyRootFilesystem:   ptrTo(true),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
		}},
	}
	addVolumes(&podSpec, spec)

	// Spread agents across nodes so one node's network does not limit the test (LOAD-8).
	if spec.Agents.SpreadAcrossNodes == nil || *spec.Agents.SpreadAcrossNodes {
		podSpec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
			MaxSkew:           1,
			TopologyKey:       "kubernetes.io/hostname",
			WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{LabelRun: run.Name}},
		}}
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentJobName(run),
			Namespace: run.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			Completions:             &completions,
			Parallelism:             &completions,
			CompletionMode:          &indexed,
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

func agentEnv(run *orbv1.OrbBenchmarkRun, settings jobSettings) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: EnvControllerURL, Value: settings.AgentAPIURL},
		{Name: EnvRunUID, Value: string(run.UID)},
		{Name: EnvRunName, Value: run.Name},
		{Name: EnvAgentToken, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: tokenSecretName(run)},
			Key:                  tokenSecretKey,
		}}},
	}
}

// addVolumes mounts credentials, CA certificates and scratch space.
// Secrets are mounted directly into the agent pods; they never pass through
// the controller (AUTH-6).
func addVolumes(podSpec *corev1.PodSpec, spec *orbv1.OrbBenchmarkSpec) {
	container := &podSpec.Containers[0]
	mountSecret := func(volume, secret, key, path, mountPath string) {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: volume,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: secret,
				Items:      []corev1.KeyToPath{{Key: key, Path: path}},
			}},
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: volume, MountPath: mountPath, ReadOnly: true})
	}

	if spec.Registry.CredentialsSecret != "" {
		mountSecret("registry-credentials", spec.Registry.CredentialsSecret,
			corev1.DockerConfigJsonKey, corev1.DockerConfigJsonKey, MountRegistryCredentials)
	}
	if static := spec.ImageSet.Static; spec.ImageSet.Type == orbv1.ImageSetStatic && static != nil && static.SourceCredentialsSecret != "" {
		mountSecret("source-credentials", static.SourceCredentialsSecret,
			corev1.DockerConfigJsonKey, corev1.DockerConfigJsonKey, MountSourceCredentials)
	}
	if spec.Registry.CASecret != "" {
		mountSecret("ca", spec.Registry.CASecret, "ca.crt", "ca.crt", MountCA)
	}

	// Scratch space for the static image set (IMG-6, IMG-9).
	scratch := &corev1.EmptyDirVolumeSource{SizeLimit: &defaultScratchSize}
	if static := spec.ImageSet.Static; spec.ImageSet.Type == orbv1.ImageSetStatic && static != nil {
		if static.StorageLimit != nil {
			limit := static.StorageLimit.DeepCopy()
			scratch.SizeLimit = &limit
		}
		if static.StorageMedium == "Memory" {
			scratch.Medium = corev1.StorageMediumMemory
		}
	}
	podSpec.Volumes = append(podSpec.Volumes,
		corev1.Volume{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: scratch}},
		// The root file system is read-only, so give the agent a writable /tmp.
		corev1.Volume{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	)
	container.VolumeMounts = append(container.VolumeMounts,
		corev1.VolumeMount{Name: "scratch", MountPath: MountScratch},
		corev1.VolumeMount{Name: "tmp", MountPath: MountTmp},
	)
}

func runLabels(run *orbv1.OrbBenchmarkRun) map[string]string {
	return map[string]string{
		LabelRun:                       run.Name,
		LabelBenchmark:                 run.Spec.BenchmarkRef,
		"app.kubernetes.io/name":       "orb",
		"app.kubernetes.io/managed-by": "orb-controller",
	}
}

func ptrTo[T any](v T) *T { return &v }
