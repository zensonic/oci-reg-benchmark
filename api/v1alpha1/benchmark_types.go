// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OrbBenchmark is a reusable benchmark definition (RUN-1). Starting it creates
// an OrbBenchmarkRun.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=orbb,categories=orb
// +kubebuilder:printcolumn:name="Registry",type=string,JSONPath=`.spec.registry.url`
// +kubebuilder:printcolumn:name="Module",type=string,JSONPath=`.spec.registry.module`
// +kubebuilder:printcolumn:name="Image set",type=string,JSONPath=`.spec.imageSet.type`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type OrbBenchmark struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec OrbBenchmarkSpec `json:"spec"`
}

// OrbBenchmarkList is a list of OrbBenchmark objects.
//
// +kubebuilder:object:root=true
type OrbBenchmarkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OrbBenchmark `json:"items"`
}

// OrbBenchmarkSpec fully describes a benchmark, so it can be run again with
// identical settings.
type OrbBenchmarkSpec struct {
	// Registry is the registry under test.
	Registry RegistrySpec `json:"registry"`

	// ImageSet is the test data: random images or a static list (section 6).
	ImageSet ImageSetSpec `json:"imageSet"`

	// Scenarios are the sub-benchmarks. They run one after another (TYPE-2).
	// +kubebuilder:validation:MinItems=1
	Scenarios []ScenarioSpec `json:"scenarios"`

	// Agents controls the agent pods that run the workers.
	// +optional
	Agents AgentSpec `json:"agents,omitempty"`

	// Cleanup decides what happens to pushed artifacts after the run (CLEAN-1).
	// +kubebuilder:default=Delete
	// +optional
	Cleanup CleanupPolicy `json:"cleanup,omitempty"`

	// Seed makes random workloads reproducible (RUN-7).
	// +kubebuilder:default=1
	// +optional
	Seed int64 `json:"seed,omitempty"`

	// Measurement controls how results are recorded.
	// +optional
	Measurement MeasurementSpec `json:"measurement,omitempty"`
}

// CleanupPolicy is what happens to pushed artifacts after a run.
// +kubebuilder:validation:Enum=Delete;Keep
type CleanupPolicy string

const (
	// CleanupDelete removes everything the run pushed.
	CleanupDelete CleanupPolicy = "Delete"
	// CleanupKeep leaves pushed artifacts in the registry.
	CleanupKeep CleanupPolicy = "Keep"
)

// RegistrySpec describes the registry under test.
type RegistrySpec struct {
	// URL of the registry, for example https://harbor.example.com.
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url"`

	// Module is the registry module with registry-specific handling (REG-6).
	// +kubebuilder:default=generic
	// +kubebuilder:validation:Enum=generic;harbor
	// +optional
	Module string `json:"module,omitempty"`

	// RepositoryPrefix is the first part of every repository the run uses.
	// For Harbor this is the project name, and the project must exist.
	// Each run pushes below <repositoryPrefix>/<run name> (IMG-12).
	// +kubebuilder:validation:MinLength=1
	RepositoryPrefix string `json:"repositoryPrefix"`

	// CredentialsSecret is a Secret of type kubernetes.io/dockerconfigjson
	// with credentials for the registry (AUTH-2). Leave empty for anonymous access.
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`

	// CASecret is a Secret with a "ca.crt" key holding extra CA certificates (AUTH-5).
	// +optional
	CASecret string `json:"caSecret,omitempty"`

	// InsecureSkipVerify turns off TLS certificate verification. Only for test
	// installations (AUTH-5).
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// ImageSetType selects random or static test data.
// +kubebuilder:validation:Enum=Random;Static
type ImageSetType string

const (
	// ImageSetRandom generates random, incompressible images (IMG-1).
	ImageSetRandom ImageSetType = "Random"
	// ImageSetStatic uses a fixed list of real images (IMG-5).
	ImageSetStatic ImageSetType = "Static"
)

// ImageSetSpec describes the test data.
type ImageSetSpec struct {
	// Type is Random or Static.
	// +kubebuilder:default=Random
	Type ImageSetType `json:"type"`

	// Random configures generated images. Used when type is Random.
	// +optional
	Random *RandomImageSet `json:"random,omitempty"`

	// Static configures the fixed image list. Used when type is Static.
	// +optional
	Static *StaticImageSet `json:"static,omitempty"`

	// PoolSize is how many images are seeded for pull, HEAD, tag and
	// deduplication operations (IMG-10). For a static set, every image is
	// seeded and this field is ignored.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +optional
	PoolSize int `json:"poolSize,omitempty"`

	// PoolSelection is how workers pick images from the seeded pool (IMG-11).
	// +kubebuilder:default=Random
	// +optional
	PoolSelection PoolSelection `json:"poolSelection,omitempty"`
}

// PoolSelection is how workers pick images from the seeded pool.
// +kubebuilder:validation:Enum=Random;RoundRobin
type PoolSelection string

const (
	// PoolSelectionRandom picks a random pool image for each operation.
	PoolSelectionRandom PoolSelection = "Random"
	// PoolSelectionRoundRobin cycles through the pool images in order.
	PoolSelectionRoundRobin PoolSelection = "RoundRobin"
)

// RandomImageSet configures generated images.
type RandomImageSet struct {
	// Layers is the number of layers per image.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +optional
	Layers int `json:"layers,omitempty"`

	// LayerSize is the size of each layer.
	LayerSize SizeDistribution `json:"layerSize"`
}

// SizeDistribution describes how layer sizes are chosen (IMG-1).
type SizeDistribution struct {
	// Distribution is Fixed (always size), Uniform (between min and max) or
	// List (a random entry from sizes).
	// +kubebuilder:default=Fixed
	// +kubebuilder:validation:Enum=Fixed;Uniform;List
	// +optional
	Distribution string `json:"distribution,omitempty"`

	// Size is the layer size for the Fixed distribution, for example 50Mi.
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`

	// Min is the smallest layer size for the Uniform distribution.
	// +optional
	Min *resource.Quantity `json:"min,omitempty"`

	// Max is the largest layer size for the Uniform distribution.
	// +optional
	Max *resource.Quantity `json:"max,omitempty"`

	// Sizes are the layer sizes for the List distribution.
	// +optional
	Sizes []resource.Quantity `json:"sizes,omitempty"`
}

// StaticImageSet configures a fixed list of real images (section 6.2).
type StaticImageSet struct {
	// Images are image references, preferably pinned by digest (IMG-8).
	// +kubebuilder:validation:MinItems=1
	Images []string `json:"images"`

	// SourceCredentialsSecret is a dockerconfigjson Secret for the registry the
	// images are downloaded from, if it needs credentials (IMG-7).
	// +optional
	SourceCredentialsSecret string `json:"sourceCredentialsSecret,omitempty"`

	// Platform is used when an image reference points to a multi-platform index.
	// +kubebuilder:default="linux/amd64"
	// +optional
	Platform string `json:"platform,omitempty"`

	// StorageLimit is the most local storage an agent may use for the static
	// set (IMG-9).
	// +kubebuilder:default="2Gi"
	// +optional
	StorageLimit *resource.Quantity `json:"storageLimit,omitempty"`

	// StorageMedium is Disk (an emptyDir on the node) or Memory (a tmpfs emptyDir).
	// +kubebuilder:default=Disk
	// +kubebuilder:validation:Enum=Disk;Memory
	// +optional
	StorageMedium string `json:"storageMedium,omitempty"`
}

// ScenarioType is the type of a sub-benchmark (TYPE-6).
// +kubebuilder:validation:Enum=load;deduplication
type ScenarioType string

const (
	// ScenarioLoad is the general load test with an operation mix.
	ScenarioLoad ScenarioType = "load"
	// ScenarioDeduplication measures pushes of content the registry already has.
	ScenarioDeduplication ScenarioType = "deduplication"
)

// ScenarioSpec is one sub-benchmark (TYPE-1).
type ScenarioSpec struct {
	// Name identifies the scenario in results. It is also used in repository names.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=40
	Name string `json:"name"`

	// Type is the scenario type.
	Type ScenarioType `json:"type"`

	// Load configures a scenario of type load.
	// +optional
	Load *LoadScenario `json:"load,omitempty"`

	// Deduplication configures a scenario of type deduplication.
	// +optional
	Deduplication *DeduplicationScenario `json:"deduplication,omitempty"`

	// Profile is the load profile: stages, warm-up, think time and timeouts.
	Profile LoadProfile `json:"profile"`
}

// Operation names used in operation mixes and in results.
const (
	OpPull         = "pull"
	OpPush         = "push"
	OpHeadManifest = "headManifest"
	OpHeadBlob     = "headBlob"
	OpTag          = "tag"
	OpListTags     = "listTags"
	OpCatalog      = "catalog"
)

// LoadScenario configures the general load test (section 7).
type LoadScenario struct {
	// Mix maps operation names to their share in percent. Allowed names:
	// pull, push, headManifest, headBlob, tag, listTags, catalog.
	// The values must add up to 100.
	// +kubebuilder:validation:MinProperties=1
	Mix map[string]int `json:"mix"`

	// ChunkSize is the size of each PATCH when pushing (OP-9).
	// +kubebuilder:default="5Mi"
	// +optional
	ChunkSize *resource.Quantity `json:"chunkSize,omitempty"`

	// PageSize is the page size for tag listing and catalog (OP-11).
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	// +optional
	PageSize int `json:"pageSize,omitempty"`

	// VerifyDigests checks pulled data against its digest (OP-10).
	// +optional
	VerifyDigests bool `json:"verifyDigests,omitempty"`
}

// Deduplication methods (DEDUP-4).
const (
	DedupExistenceCheck = "existenceCheck"
	DedupCrossRepoMount = "crossRepoMount"
	DedupReupload       = "reupload"
)

// Deduplication scopes (DEDUP-3).
const (
	DedupScopeSameRepository   = "SameRepository"
	DedupScopeSameProject      = "SameProject"
	DedupScopeDifferentProject = "DifferentProject"
)

// DeduplicationScenario configures the deduplication test (section 5.1).
type DeduplicationScenario struct {
	// Scope is where the existing blobs live relative to the push target.
	// +kubebuilder:default=SameProject
	// +kubebuilder:validation:Enum=SameRepository;SameProject;DifferentProject
	// +optional
	Scope string `json:"scope,omitempty"`

	// TargetProject is the project (first repository path part) pushes go to
	// when scope is DifferentProject. For Harbor, the project must exist.
	// +optional
	TargetProject string `json:"targetProject,omitempty"`

	// Methods are the push methods to measure (DEDUP-4).
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Enum=existenceCheck;crossRepoMount;reupload
	Methods []string `json:"methods"`

	// Baseline also pushes unique content of the same sizes, for comparison (DEDUP-7).
	// +kubebuilder:default=true
	// +optional
	Baseline *bool `json:"baseline,omitempty"`

	// ChunkSize is the size of each PATCH when data is uploaded.
	// +kubebuilder:default="5Mi"
	// +optional
	ChunkSize *resource.Quantity `json:"chunkSize,omitempty"`
}

// LoadProfile describes how the number of workers changes over time (section 10).
type LoadProfile struct {
	// Stages run in order. Worker counts change linearly within a stage (LOAD-1).
	// +kubebuilder:validation:MinItems=1
	Stages []Stage `json:"stages"`

	// Warmup is a period at the start whose measurements are kept separate (LOAD-5).
	// +optional
	Warmup *metav1.Duration `json:"warmup,omitempty"`

	// ThinkTime is a pause between operations for each worker (LOAD-6).
	// +optional
	ThinkTime *metav1.Duration `json:"thinkTime,omitempty"`

	// OperationTimeout is the longest a single operation may take (LOAD-7).
	// +kubebuilder:default="5m"
	// +optional
	OperationTimeout *metav1.Duration `json:"operationTimeout,omitempty"`

	// ConnectionReuse is PerWorker (keep connections and tokens) or
	// PerOperation (a cold client for every operation) (LOAD-4).
	// +kubebuilder:default=PerWorker
	// +kubebuilder:validation:Enum=PerWorker;PerOperation
	// +optional
	ConnectionReuse string `json:"connectionReuse,omitempty"`
}

// Stage is one segment of a load profile.
type Stage struct {
	// Duration of the stage.
	Duration metav1.Duration `json:"duration"`

	// Workers is the worker count at the end of the stage. The count changes
	// linearly from the previous stage's count (0 before the first stage).
	// +kubebuilder:validation:Minimum=0
	Workers int `json:"workers"`
}

// AgentSpec controls the agent pods.
type AgentSpec struct {
	// WorkersPerAgent is the most workers one agent pod runs (LOAD-2).
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +optional
	WorkersPerAgent int `json:"workersPerAgent,omitempty"`

	// Resources for the agent container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector for agent pods.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for agent pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// SpreadAcrossNodes spreads agents across nodes (LOAD-8).
	// +kubebuilder:default=true
	// +optional
	SpreadAcrossNodes *bool `json:"spreadAcrossNodes,omitempty"`
}

// MeasurementSpec controls how results are recorded.
type MeasurementSpec struct {
	// Interval between metric reports from agents (ARCH-6).
	// +kubebuilder:default="1s"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// RawSamples also stores every single operation in SQLite (RES-9).
	// +optional
	RawSamples bool `json:"rawSamples,omitempty"`
}
