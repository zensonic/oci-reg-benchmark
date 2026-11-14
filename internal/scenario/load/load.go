// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package load implements the "load" scenario type: a general load test with
// a configurable mix of the seven registry operations (section 7).
package load

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/imageset"
	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/scenario"
)

// Scenario is a load scenario.
type Scenario struct {
	env  *scenario.Env
	spec orbv1.ScenarioSpec

	mix       []weightedOp
	chunkSize int64
	pool      []scenario.PoolImage
}

type weightedOp struct {
	operation string
	share     int
}

// poolOperations need seeded images in the pool repository.
var poolOperations = map[string]bool{
	orbv1.OpPull: true, orbv1.OpHeadManifest: true, orbv1.OpHeadBlob: true,
	orbv1.OpTag: true, orbv1.OpListTags: true,
}

// New creates a load scenario from its spec.
func New(env *scenario.Env, spec orbv1.ScenarioSpec) (*Scenario, error) {
	if spec.Load == nil {
		return nil, errors.New("load settings are missing")
	}
	s := &Scenario{env: env, spec: spec, chunkSize: spec.Load.ChunkSize.Value()}
	for op, share := range spec.Load.Mix {
		if share > 0 {
			s.mix = append(s.mix, weightedOp{operation: op, share: share})
		}
	}
	// Sorted so the same seed picks the same operations (RUN-7).
	sort.Slice(s.mix, func(i, j int) bool { return s.mix[i].operation < s.mix[j].operation })
	if len(s.mix) == 0 {
		return nil, errors.New("the operation mix is empty")
	}
	if minSize, maxSize := env.Module.ChunkSizeLimits(); (minSize > 0 && s.chunkSize < minSize) || (maxSize > 0 && s.chunkSize > maxSize) {
		return nil, fmt.Errorf("chunk size %d is outside the registry's limits (%d-%d)", s.chunkSize, minSize, maxSize)
	}
	return s, nil
}

// Name returns the scenario name.
func (s *Scenario) Name() string { return s.spec.Name }

// Operations returns the operations in the mix.
func (s *Scenario) Operations() []string {
	ops := make([]string, len(s.mix))
	for i, m := range s.mix {
		ops[i] = m.operation
	}
	return ops
}

// Repositories returns the pool and push repositories.
func (s *Scenario) Repositories() []string {
	return []string{s.env.Layout.Pool(s.spec.Name), s.env.Layout.Push(s.spec.Name)}
}

func (s *Scenario) needsPool() bool {
	for _, m := range s.mix {
		if poolOperations[m.operation] {
			return true
		}
	}
	return false
}

// Seed pushes this agent's share of the pool, if the mix needs one.
func (s *Scenario) Seed(ctx context.Context) error {
	if !s.needsPool() {
		return nil
	}
	return s.env.SeedPool(ctx, s.spec.Name, s.env.Layout.Pool(s.spec.Name), s.chunkSize)
}

// Prepare resolves the pool manifests.
func (s *Scenario) Prepare(ctx context.Context) error {
	if !s.needsPool() {
		return nil
	}
	pool, err := s.env.ResolvePool(ctx, s.env.Layout.Pool(s.spec.Name))
	s.pool = pool
	return err
}

// NewWorker creates a worker.
func (s *Scenario) NewWorker(id int) scenario.Worker {
	rng := s.env.WorkerRand(s.spec.Name, id)
	w := &worker{
		s:       s,
		id:      id,
		rng:     rng,
		clients: scenario.NewClientHolder(s.env, s.spec.Profile),
	}
	if len(s.pool) > 0 {
		w.picker = scenario.NewPoolPicker(s.pool, s.env.Spec().ImageSet.PoolSelection, rng, id)
	}
	return w
}

// worker runs the operation mix for one simulated client.
type worker struct {
	s       *Scenario
	id      int
	n       int
	rng     *rand.Rand
	clients *scenario.ClientHolder
	picker  *scenario.PoolPicker
}

func (w *worker) Close() { w.clients.Close() }

// RunOnce picks an operation from the mix and runs it.
func (w *worker) RunOnce(ctx context.Context) metrics.Result {
	op := w.pickOperation()
	w.n++

	client, done, err := w.clients.Get()
	if err != nil {
		return scenario.Failed(op, err)
	}
	defer done()

	switch op {
	case orbv1.OpPull:
		return w.pull(ctx, client)
	case orbv1.OpPush:
		return w.push(ctx, client)
	case orbv1.OpHeadManifest:
		return w.headManifest(ctx, client)
	case orbv1.OpHeadBlob:
		return w.headBlob(ctx, client)
	case orbv1.OpTag:
		return w.tag(ctx, client)
	case orbv1.OpListTags:
		return w.listTags(ctx, client)
	case orbv1.OpCatalog:
		return w.catalog(ctx, client)
	default:
		return scenario.Failed(op, fmt.Errorf("unknown operation %q", op))
	}
}

func (w *worker) pickOperation() string {
	roll := w.rng.IntN(100)
	for _, m := range w.s.mix {
		if roll < m.share {
			return m.operation
		}
		roll -= m.share
	}
	return w.s.mix[len(w.s.mix)-1].operation
}

// pull pulls a pool image. Latency ends at the first blob byte (LAT-2).
func (w *worker) pull(ctx context.Context, client *core.Client) metrics.Result {
	img := w.picker.Pick()
	timing := core.StartTiming()
	pulled, err := client.PullImage(ctx, timing, w.s.env.Layout.Pool(w.s.spec.Name), img.Tag, w.s.env.Platform(), w.s.spec.Load.VerifyDigests)
	if err != nil {
		return scenario.Finish(orbv1.OpPull, timing, time.Time{}, err)
	}
	res := scenario.Finish(orbv1.OpPull, timing, pulled.FirstBlobByte, nil)
	res.Bytes, res.TransferTime = pulled.Bytes, pulled.TransferTime
	return res
}

// push pushes a new image with unique content (IMG-4, OP-8). Latency ends
// when the first chunk is acknowledged (LAT-3).
func (w *worker) push(ctx context.Context, client *core.Client) metrics.Result {
	var img core.Image
	if static := w.s.env.Static; static != nil {
		img = static.Images[(w.id+w.n)%len(static.Images)]
	} else {
		img = w.s.env.Random.Image(imageset.Key(w.s.spec.Name, "push", w.s.env.Agent, w.id, w.n))
	}

	timing := core.StartTiming()
	pushed, err := client.PushImage(ctx, timing, w.s.env.Layout.Push(w.s.spec.Name), w.s.env.Tag("", w.id, w.n), img, w.s.chunkSize)
	if err != nil {
		return scenario.Finish(orbv1.OpPush, timing, time.Time{}, err)
	}
	res := scenario.Finish(orbv1.OpPush, timing, pushed.FirstChunkAck, nil)
	res.Bytes, res.TransferTime = pushed.Bytes, pushed.TransferTime
	return res
}

// headManifest checks a pool manifest. Latency ends at the first response byte (LAT-4).
func (w *worker) headManifest(ctx context.Context, client *core.Client) metrics.Result {
	img := w.picker.Pick()
	timing := core.StartTiming()
	head, err := client.HeadManifest(ctx, timing, w.s.env.Layout.Pool(w.s.spec.Name), img.Tag)
	if err == nil && !head.Exists {
		err = fmt.Errorf("manifest %s not found", img.Tag)
	}
	if err != nil {
		return scenario.Finish(orbv1.OpHeadManifest, timing, time.Time{}, err)
	}
	return scenario.Finish(orbv1.OpHeadManifest, timing, head.FirstByte, nil)
}

// headBlob checks a random layer of a pool image (LAT-4).
func (w *worker) headBlob(ctx context.Context, client *core.Client) metrics.Result {
	img := w.picker.Pick()
	blobs := img.Manifest.Blobs()
	blob := blobs[w.rng.IntN(len(blobs))]

	timing := core.StartTiming()
	head, err := client.HeadBlob(ctx, timing, w.s.env.Layout.Pool(w.s.spec.Name), blob.Digest)
	if err == nil && !head.Exists {
		err = fmt.Errorf("blob %s not found", blob.Digest)
	}
	if err != nil {
		return scenario.Finish(orbv1.OpHeadBlob, timing, time.Time{}, err)
	}
	return scenario.Finish(orbv1.OpHeadBlob, timing, head.FirstByte, nil)
}

// tag puts an existing pool manifest under a new tag (OP-5). Latency ends at
// the response to the PUT (LAT-5).
func (w *worker) tag(ctx context.Context, client *core.Client) metrics.Result {
	img := w.picker.Pick()
	timing := core.StartTiming()
	_, err := client.PutManifest(ctx, timing, w.s.env.Layout.Pool(w.s.spec.Name), w.s.env.Tag("t-", w.id, w.n), img.MediaType, img.Raw)
	return scenario.Finish(orbv1.OpTag, timing, time.Time{}, err)
}

// listTags lists the pool repository's tags (OP-6, LAT-4).
func (w *worker) listTags(ctx context.Context, client *core.Client) metrics.Result {
	timing := core.StartTiming()
	list, err := client.ListTags(ctx, timing, w.s.env.Layout.Pool(w.s.spec.Name), w.s.spec.Load.PageSize, false)
	return listResult(orbv1.OpListTags, timing, list, err)
}

// catalog lists the registry's repositories (OP-7, LAT-4).
func (w *worker) catalog(ctx context.Context, client *core.Client) metrics.Result {
	timing := core.StartTiming()
	list, err := client.Catalog(ctx, timing, w.s.spec.Load.PageSize, false)
	return listResult(orbv1.OpCatalog, timing, list, err)
}

func listResult(op string, timing *core.Timing, list *core.ListResult, err error) metrics.Result {
	if err != nil {
		return scenario.Finish(op, timing, time.Time{}, err)
	}
	res := scenario.Finish(op, timing, list.FirstByte, nil)
	res.Counters = map[string]int64{"pages": int64(list.Pages)}
	return res
}
