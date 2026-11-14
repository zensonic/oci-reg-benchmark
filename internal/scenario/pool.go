// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"math/rand/v2"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/imageset"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
)

// PoolImage is one seeded image, resolved after seeding.
type PoolImage struct {
	// Index is the pool item number k; Tag is "img-<k>".
	Index     int
	Tag       string
	Digest    string
	Raw       []byte
	MediaType string
	Manifest  *core.Manifest
}

// PoolTag is the tag of pool item k.
func PoolTag(k int) string { return fmt.Sprintf("img-%d", k) }

// PoolSize is the number of seeded images: every static image, or
// imageSet.poolSize random images (IMG-10).
func (e *Env) PoolSize() int {
	if e.Static != nil {
		return len(e.Static.Images)
	}
	return max(e.Spec().ImageSet.PoolSize, 1)
}

// PoolImageSource returns the content of pool item k of a scenario.
func (e *Env) PoolImageSource(scenarioName string, k int) core.Image {
	if e.Static != nil {
		return e.Static.Images[k%len(e.Static.Images)]
	}
	return e.Random.Image(imageset.Key(scenarioName, "pool", k))
}

// SeedPool pushes the pool items this agent owns into a repository (IMG-10).
func (e *Env) SeedPool(ctx context.Context, scenarioName, repository string, chunkSize int64) error {
	client, err := e.NewClient()
	if err != nil {
		return err
	}
	defer client.Close()

	for k := 0; k < e.PoolSize(); k++ {
		if !e.Owns(k) {
			continue
		}
		img := e.PoolImageSource(scenarioName, k)
		if _, err := client.PushImage(ctx, core.StartTiming(), repository, PoolTag(k), img, chunkSize); err != nil {
			return fmt.Errorf("seeding %s:%s: %w", repository, PoolTag(k), err)
		}
	}
	return nil
}

// ResolvePool fetches the manifests of all pool items, seeded by any agent.
func (e *Env) ResolvePool(ctx context.Context, repository string) ([]PoolImage, error) {
	client, err := e.NewClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	images := make([]PoolImage, e.PoolSize())
	for k := range images {
		result, err := client.GetManifest(ctx, core.StartTiming(), repository, PoolTag(k))
		if err != nil {
			return nil, fmt.Errorf("resolving seeded image %s:%s: %w", repository, PoolTag(k), err)
		}
		images[k] = PoolImage{
			Index:     k,
			Tag:       PoolTag(k),
			Digest:    result.Digest,
			Raw:       result.Raw,
			MediaType: result.MediaType,
			Manifest:  result.Manifest,
		}
	}
	return images, nil
}

// PoolPicker picks pool images for one worker (IMG-11).
type PoolPicker struct {
	images     []PoolImage
	roundRobin bool
	next       int
	rng        *rand.Rand
}

// NewPoolPicker creates a picker. Round-robin workers start at different
// offsets so they do not all hit the same image at once.
func NewPoolPicker(images []PoolImage, selection orbv1.PoolSelection, rng *rand.Rand, worker int) *PoolPicker {
	return &PoolPicker{
		images:     images,
		roundRobin: selection == orbv1.PoolSelectionRoundRobin,
		next:       worker,
		rng:        rng,
	}
}

// Pick returns the next pool image.
func (p *PoolPicker) Pick() PoolImage {
	if p.roundRobin {
		img := p.images[p.next%len(p.images)]
		p.next++
		return img
	}
	return p.images[p.rng.IntN(len(p.images))]
}
