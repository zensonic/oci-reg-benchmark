// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package dedup implements the "deduplication" scenario type (section 5.1).
// It measures how quickly the registry handles pushes of blobs it already
// has, using three push methods, and compares them with a baseline push of
// unique content of the same sizes.
package dedup

import (
	"context"
	"errors"
	"fmt"
	"time"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/imageset"
	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/scenario"
)

// OpBaseline is the operation name of the baseline push (DEDUP-7).
const OpBaseline = "baseline"

// CounterMountMisses counts mount requests that did not mount (DEDUP-6).
const CounterMountMisses = "mountMisses"

// Scenario is a deduplication scenario.
type Scenario struct {
	env       *scenario.Env
	spec      orbv1.ScenarioSpec
	settings  *orbv1.DeduplicationScenario
	chunkSize int64
	ops       []string

	source, target string
	pool           []scenario.PoolImage
}

// New creates a deduplication scenario from its spec.
func New(env *scenario.Env, spec orbv1.ScenarioSpec) (*Scenario, error) {
	d := spec.Deduplication
	if d == nil || len(d.Methods) == 0 {
		return nil, errors.New("deduplication.methods must list at least one method")
	}
	s := &Scenario{
		env:       env,
		spec:      spec,
		settings:  d,
		chunkSize: d.ChunkSize.Value(),
		source:    env.Layout.Source(spec.Name),
		target:    env.Layout.Target(spec.Name, d),
	}
	for _, method := range d.Methods {
		switch method {
		case orbv1.DedupExistenceCheck, orbv1.DedupCrossRepoMount, orbv1.DedupReupload:
			s.ops = append(s.ops, method)
		default:
			return nil, fmt.Errorf("unknown deduplication method %q", method)
		}
	}
	if d.Baseline == nil || *d.Baseline {
		s.ops = append(s.ops, OpBaseline)
	}
	return s, nil
}

// Name returns the scenario name.
func (s *Scenario) Name() string { return s.spec.Name }

// Operations returns the measured methods, plus the baseline.
func (s *Scenario) Operations() []string { return append([]string{}, s.ops...) }

// Repositories returns the source and target repositories.
func (s *Scenario) Repositories() []string {
	if s.source == s.target {
		return []string{s.source}
	}
	return []string{s.source, s.target}
}

// Seed pushes this agent's share of the existing content to the source
// repository (DEDUP-2).
func (s *Scenario) Seed(ctx context.Context) error {
	return s.env.SeedPool(ctx, s.spec.Name, s.source, s.chunkSize)
}

// Prepare resolves the manifests of the seeded content.
func (s *Scenario) Prepare(ctx context.Context) error {
	pool, err := s.env.ResolvePool(ctx, s.source)
	s.pool = pool
	return err
}

// NewWorker creates a worker. Each worker cycles through the methods.
func (s *Scenario) NewWorker(id int) scenario.Worker {
	rng := s.env.WorkerRand(s.spec.Name, id)
	return &worker{
		s:       s,
		id:      id,
		next:    id, // workers start on different methods
		clients: scenario.NewClientHolder(s.env, s.spec.Profile),
		picker:  scenario.NewPoolPicker(s.pool, s.env.Spec().ImageSet.PoolSelection, rng, id),
	}
}

// blobSource returns the content of one blob of pool item k, so it can be
// uploaded again: from disk for a static set, or regenerated from its seed
// for random images.
func (s *Scenario) blobSource(item scenario.PoolImage, blob core.Descriptor) (core.BlobSource, error) {
	if s.env.Static != nil {
		img := s.env.Static.Images[item.Index%len(s.env.Static.Images)]
		return img.Blob(blob), nil
	}

	img := s.env.Random.Image(imageset.Key(s.spec.Name, "pool", item.Index))
	if blob.Digest == item.Manifest.Config.Digest {
		data, mediaType, err := img.Config(item.Manifest.Layers)
		if err != nil {
			return nil, err
		}
		return core.BytesBlob(data, mediaType), nil
	}
	layers := img.Layers()
	for i, layer := range item.Manifest.Layers {
		if layer.Digest == blob.Digest && i < len(layers) {
			return layers[i], nil
		}
	}
	return nil, fmt.Errorf("blob %s is not part of seeded image %s", blob.Digest, item.Tag)
}

// worker pushes existing content with each method in turn.
type worker struct {
	s       *Scenario
	id      int
	n       int
	next    int
	clients *scenario.ClientHolder
	picker  *scenario.PoolPicker
}

func (w *worker) Close() { w.clients.Close() }

// RunOnce runs the next method on a pool image.
func (w *worker) RunOnce(ctx context.Context) metrics.Result {
	op := w.s.ops[w.next%len(w.s.ops)]
	w.next++
	w.n++

	client, done, err := w.clients.Get()
	if err != nil {
		return scenario.Failed(op, err)
	}
	defer done()

	item := w.picker.Pick()
	if op == OpBaseline {
		return w.baseline(ctx, client, item)
	}
	return w.dedupPush(ctx, client, op, item)
}

// dedupPush pushes an image whose blobs already exist in the registry.
// Latency ends when the registry accepts the manifest (DEDUP-5); the time of
// each per-blob deduplication step is recorded too.
func (w *worker) dedupPush(ctx context.Context, client *core.Client, op string, item scenario.PoolImage) metrics.Result {
	timing := core.StartTiming()
	var steps []time.Duration
	var bytes int64
	var transfer time.Duration
	misses := int64(0)

	record := func(upload *core.UploadResult) {
		bytes += upload.Descriptor.Size
		transfer += upload.TransferEnd.Sub(upload.TransferStart)
	}
	fail := func(err error) metrics.Result {
		return scenario.Finish(op, timing, time.Time{}, err)
	}

	for _, blob := range item.Manifest.Blobs() {
		stepStart := time.Now()
		switch op {
		case orbv1.DedupExistenceCheck:
			// Like Docker and containerd: HEAD first, upload only if missing.
			head, err := client.HeadBlob(ctx, timing, w.s.target, blob.Digest)
			if err != nil {
				return fail(err)
			}
			if !head.Exists {
				upload, err := w.upload(ctx, client, timing, item, blob, nil)
				if err != nil {
					return fail(err)
				}
				record(upload)
			}

		case orbv1.DedupCrossRepoMount:
			mount, err := client.MountBlob(ctx, timing, w.s.target, blob.Digest, w.s.source)
			if err != nil {
				return fail(err)
			}
			if !mount.Mounted {
				// A mount miss: finish with a normal upload (DEDUP-6).
				misses++
				upload, err := w.upload(ctx, client, timing, item, blob, mount)
				if err != nil {
					return fail(err)
				}
				record(upload)
			}

		case orbv1.DedupReupload:
			upload, err := w.upload(ctx, client, timing, item, blob, nil)
			if err != nil {
				return fail(err)
			}
			record(upload)
		}
		steps = append(steps, time.Since(stepStart))
	}

	_, err := client.PutManifest(ctx, timing, w.s.target, w.s.env.Tag("", w.id, w.n), item.MediaType, item.Raw)
	res := scenario.Finish(op, timing, time.Time{}, err)
	res.BlobSteps = steps
	res.Bytes, res.TransferTime = bytes, transfer
	if op == orbv1.DedupCrossRepoMount {
		res.Counters = map[string]int64{CounterMountMisses: misses}
	}
	return res
}

// upload sends a blob in full, either to a new upload session or to the
// session the registry opened on a mount miss.
func (w *worker) upload(ctx context.Context, client *core.Client, timing *core.Timing, item scenario.PoolImage, blob core.Descriptor, miss *core.MountResult) (*core.UploadResult, error) {
	src, err := w.s.blobSource(item, blob)
	if err != nil {
		return nil, err
	}
	if miss != nil && miss.UploadURL != nil {
		return client.UploadToSession(ctx, timing, w.s.target, miss.UploadURL, src, w.s.chunkSize)
	}
	return client.UploadBlob(ctx, timing, w.s.target, src, w.s.chunkSize)
}

// baseline pushes unique content with the same layer sizes as the pool
// image, for comparison (DEDUP-7). Latency ends when the manifest is accepted.
func (w *worker) baseline(ctx context.Context, client *core.Client, item scenario.PoolImage) metrics.Result {
	sizes := make([]int64, len(item.Manifest.Layers))
	for i, l := range item.Manifest.Layers {
		sizes[i] = l.Size
	}
	img := w.s.env.Random.ImageWithSizes(imageset.Key(w.s.spec.Name, "baseline", w.s.env.Agent, w.id, w.n), sizes)

	timing := core.StartTiming()
	pushed, err := client.PushImage(ctx, timing, w.s.target, w.s.env.Tag("b-", w.id, w.n), img, w.s.chunkSize)
	res := scenario.Finish(OpBaseline, timing, time.Time{}, err)
	if err == nil {
		res.Bytes, res.TransferTime = pushed.Bytes, pushed.TransferTime
	}
	return res
}
