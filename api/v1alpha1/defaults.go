// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Default values used when a field is not set. The CRD schema sets most of
// these as well, but nested optional structs are not defaulted by the API
// server, so the controller and agents call Default on the spec snapshot.
const (
	DefaultWorkersPerAgent  = 50
	DefaultPoolSize         = 10
	DefaultLayers           = 3
	DefaultPageSize         = 100
	DefaultChunkSize        = 5 * 1024 * 1024
	DefaultOperationTimeout = 5 * time.Minute
	DefaultInterval         = time.Second
	DefaultPlatform         = "linux/amd64"
	DefaultStorageLimit     = "2Gi"
)

// Default fills in every unset field with its default value.
func (s *OrbBenchmarkSpec) Default() {
	if s.Registry.Module == "" {
		s.Registry.Module = "generic"
	}
	if s.Cleanup == "" {
		s.Cleanup = CleanupDelete
	}
	if s.Seed == 0 {
		s.Seed = 1
	}
	if s.Agents.WorkersPerAgent == 0 {
		s.Agents.WorkersPerAgent = DefaultWorkersPerAgent
	}
	if s.Agents.SpreadAcrossNodes == nil {
		s.Agents.SpreadAcrossNodes = ptr(true)
	}
	if s.Measurement.Interval == nil {
		s.Measurement.Interval = &metav1.Duration{Duration: DefaultInterval}
	}
	s.ImageSet.defaultImageSet()
	for i := range s.Scenarios {
		s.Scenarios[i].defaultScenario()
	}
}

func (s *ImageSetSpec) defaultImageSet() {
	if s.Type == "" {
		s.Type = ImageSetRandom
	}
	if s.PoolSize == 0 {
		s.PoolSize = DefaultPoolSize
	}
	if s.PoolSelection == "" {
		s.PoolSelection = PoolSelectionRandom
	}
	if s.Type == ImageSetRandom {
		if s.Random == nil {
			s.Random = &RandomImageSet{}
		}
		if s.Random.Layers == 0 {
			s.Random.Layers = DefaultLayers
		}
		if s.Random.LayerSize.Distribution == "" {
			s.Random.LayerSize.Distribution = "Fixed"
		}
		if s.Random.LayerSize.Distribution == "Fixed" && s.Random.LayerSize.Size == nil {
			s.Random.LayerSize.Size = ptr(resource.MustParse("10Mi"))
		}
	}
	if s.Type == ImageSetStatic && s.Static != nil {
		if s.Static.Platform == "" {
			s.Static.Platform = DefaultPlatform
		}
		if s.Static.StorageLimit == nil {
			s.Static.StorageLimit = ptr(resource.MustParse(DefaultStorageLimit))
		}
		if s.Static.StorageMedium == "" {
			s.Static.StorageMedium = "Disk"
		}
	}
}

func (s *ScenarioSpec) defaultScenario() {
	p := &s.Profile
	if p.OperationTimeout == nil {
		p.OperationTimeout = &metav1.Duration{Duration: DefaultOperationTimeout}
	}
	if p.ConnectionReuse == "" {
		p.ConnectionReuse = "PerWorker"
	}
	switch s.Type {
	case ScenarioLoad:
		if s.Load == nil {
			s.Load = &LoadScenario{Mix: map[string]int{OpPull: 100}}
		}
		if s.Load.ChunkSize == nil {
			s.Load.ChunkSize = resource.NewQuantity(DefaultChunkSize, resource.BinarySI)
		}
		if s.Load.PageSize == 0 {
			s.Load.PageSize = DefaultPageSize
		}
	case ScenarioDeduplication:
		if s.Deduplication == nil {
			s.Deduplication = &DeduplicationScenario{Methods: []string{DedupExistenceCheck}}
		}
		d := s.Deduplication
		if d.Scope == "" {
			d.Scope = DedupScopeSameProject
		}
		if d.Baseline == nil {
			d.Baseline = ptr(true)
		}
		if d.ChunkSize == nil {
			d.ChunkSize = resource.NewQuantity(DefaultChunkSize, resource.BinarySI)
		}
	}
}

// Validate checks rules the CRD schema cannot express. It expects a
// defaulted spec.
func (s *OrbBenchmarkSpec) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if s.ImageSet.Type == ImageSetStatic && (s.ImageSet.Static == nil || len(s.ImageSet.Static.Images) == 0) {
		add("imageSet.static.images must list at least one image when imageSet.type is Static")
	}
	if s.ImageSet.Type == ImageSetRandom {
		if err := s.ImageSet.Random.LayerSize.validate(); err != nil {
			add("imageSet.random.layerSize: %v", err)
		}
	}

	names := map[string]bool{}
	for _, sc := range s.Scenarios {
		if names[sc.Name] {
			add("scenario name %q is used more than once", sc.Name)
		}
		names[sc.Name] = true
		if err := sc.validate(); err != nil {
			add("scenario %q: %v", sc.Name, err)
		}
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (d SizeDistribution) validate() error {
	switch d.Distribution {
	case "Fixed":
		if d.Size == nil || d.Size.Value() <= 0 {
			return errors.New("size must be set and positive for the Fixed distribution")
		}
	case "Uniform":
		if d.Min == nil || d.Max == nil || d.Min.Value() <= 0 || d.Max.Value() < d.Min.Value() {
			return errors.New("min and max must be set, positive, and min <= max for the Uniform distribution")
		}
	case "List":
		if len(d.Sizes) == 0 {
			return errors.New("sizes must list at least one size for the List distribution")
		}
	default:
		return fmt.Errorf("unknown distribution %q", d.Distribution)
	}
	return nil
}

// KnownLoadOperations are the operation names allowed in a load mix.
var KnownLoadOperations = []string{OpPull, OpPush, OpHeadManifest, OpHeadBlob, OpTag, OpListTags, OpCatalog}

func (s ScenarioSpec) validate() error {
	switch s.Type {
	case ScenarioLoad:
		if s.Load == nil {
			return errors.New("load settings are missing")
		}
		total := 0
		for op, share := range s.Load.Mix {
			if !contains(KnownLoadOperations, op) {
				return fmt.Errorf("unknown operation %q in mix (allowed: %s)", op, strings.Join(KnownLoadOperations, ", "))
			}
			if share < 0 {
				return fmt.Errorf("operation %q has a negative share", op)
			}
			total += share
		}
		if total != 100 {
			return fmt.Errorf("the operation mix adds up to %d, not 100", total)
		}
	case ScenarioDeduplication:
		d := s.Deduplication
		if d == nil || len(d.Methods) == 0 {
			return errors.New("deduplication.methods must list at least one method")
		}
		if d.Scope == DedupScopeDifferentProject && d.TargetProject == "" {
			return errors.New("deduplication.targetProject is required when scope is DifferentProject")
		}
	default:
		return fmt.Errorf("unknown scenario type %q", s.Type)
	}
	if len(s.Profile.Stages) == 0 {
		return errors.New("profile.stages must have at least one stage")
	}
	return nil
}

// MaxWorkers is the highest worker count in any stage of any scenario.
func (s *OrbBenchmarkSpec) MaxWorkers() int {
	highest := 0
	for _, sc := range s.Scenarios {
		for _, st := range sc.Profile.Stages {
			highest = max(highest, st.Workers)
		}
	}
	return highest
}

// AgentCount is the number of agent pods needed for the spec (LOAD-2).
func (s *OrbBenchmarkSpec) AgentCount() int {
	perAgent := max(s.Agents.WorkersPerAgent, 1)
	return max(1, (s.MaxWorkers()+perAgent-1)/perAgent)
}

// ApplyOverrides returns a copy of the spec with the run overrides applied (RUN-4).
func (s *OrbBenchmarkSpec) ApplyOverrides(o *RunOverrides) (*OrbBenchmarkSpec, error) {
	out := s.DeepCopy()
	if o == nil {
		return out, nil
	}
	if o.Cleanup != nil {
		out.Cleanup = *o.Cleanup
	}
	if o.Seed != nil {
		out.Seed = *o.Seed
	}
	if o.RepositoryPrefix != nil {
		out.Registry.RepositoryPrefix = *o.RepositoryPrefix
	}
	if o.WorkersPerAgent != nil {
		out.Agents.WorkersPerAgent = *o.WorkersPerAgent
	}
	for name, stages := range o.Stages {
		found := false
		for i := range out.Scenarios {
			if out.Scenarios[i].Name == name {
				out.Scenarios[i].Profile.Stages = stages
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("stage override names unknown scenario %q", name)
		}
	}
	return out, nil
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func ptr[T any](v T) *T { return &v }
