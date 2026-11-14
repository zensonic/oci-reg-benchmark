// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package imageset provides ORB's test data (section 6 of the requirements):
// random images that are generated on the fly, and a static set of real
// images that is downloaded before measuring starts.
package imageset

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand/v2"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
)

// Key turns the parts that identify an image (for example the scenario name,
// a purpose and a counter) into a number. The same parts always give the
// same key, so random content can be generated again later (RUN-7).
func Key(parts ...any) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		fmt.Fprintf(h, "%v\x00", p)
	}
	return h.Sum64()
}

// Random generates random, incompressible images (IMG-1, IMG-2). Content is
// derived from the benchmark seed and an image key, so it is reproducible
// and can be regenerated, for example after a mount miss (DEDUP-6).
type Random struct {
	layers int
	sizes  orbv1.SizeDistribution
	seed   int64
}

// NewRandom creates a generator. spec may be nil when only ImageWithSizes is
// needed, for example for the deduplication baseline with a static image set.
func NewRandom(spec *orbv1.RandomImageSet, seed int64) *Random {
	r := &Random{seed: seed}
	if spec != nil {
		r.layers = spec.Layers
		r.sizes = spec.LayerSize
	}
	return r
}

// Image returns the image for a key, with layer sizes drawn from the
// configured distribution.
func (r *Random) Image(key uint64) *RandomImage {
	rng := rand.New(rand.NewPCG(uint64(r.seed), key))
	sizes := make([]int64, max(r.layers, 1))
	for i := range sizes {
		sizes[i] = r.layerSize(rng)
	}
	return r.ImageWithSizes(key, sizes)
}

// ImageWithSizes returns an image with exactly the given layer sizes.
func (r *Random) ImageWithSizes(key uint64, sizes []int64) *RandomImage {
	img := &RandomImage{key: key}
	for i, size := range sizes {
		img.layers = append(img.layers, &RandomBlob{size: size, seed: r.contentSeed(key, i)})
	}
	return img
}

// layerSize draws one layer size from the distribution (IMG-1).
func (r *Random) layerSize(rng *rand.Rand) int64 {
	switch r.sizes.Distribution {
	case "Uniform":
		lo, hi := r.sizes.Min.Value(), r.sizes.Max.Value()
		return lo + rng.Int64N(hi-lo+1)
	case "List":
		return r.sizes.Sizes[rng.IntN(len(r.sizes.Sizes))].Value()
	default:
		if r.sizes.Size == nil {
			return 1 << 20
		}
		return r.sizes.Size.Value()
	}
}

// contentSeed derives the 32-byte ChaCha8 seed of one layer.
func (r *Random) contentSeed(key uint64, layer int) [32]byte {
	var buf [24]byte
	binary.LittleEndian.PutUint64(buf[0:], uint64(r.seed))
	binary.LittleEndian.PutUint64(buf[8:], key)
	binary.LittleEndian.PutUint64(buf[16:], uint64(layer))
	return sha256.Sum256(buf[:])
}

// RandomImage is one generated image. It implements core.Image.
type RandomImage struct {
	key    uint64
	layers []*RandomBlob
}

var _ core.Image = (*RandomImage)(nil)

// Layers returns the layer content.
func (i *RandomImage) Layers() []core.BlobSource {
	out := make([]core.BlobSource, len(i.layers))
	for n, l := range i.layers {
		out[n] = l
	}
	return out
}

// LayerSizes returns the size of each layer.
func (i *RandomImage) LayerSizes() []int64 {
	sizes := make([]int64, len(i.layers))
	for n, l := range i.layers {
		sizes[n] = l.size
	}
	return sizes
}

// Config returns a minimal, valid OCI image config. The image key is stored
// as a label, so every image has a unique config blob.
func (i *RandomImage) Config(layers []core.Descriptor) ([]byte, string, error) {
	diffIDs := make([]string, len(layers))
	for n, l := range layers {
		diffIDs[n] = l.Digest
	}
	config := map[string]any{
		"architecture": "amd64",
		"os":           "linux",
		"config": map[string]any{
			"Labels": map[string]string{"orb.kubectl.dk/image-key": fmt.Sprint(i.key)},
		},
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs},
	}
	data, err := json.Marshal(config)
	return data, core.MediaTypeOCIConfig, err
}

// Manifest returns an OCI image manifest for the config and layers.
func (i *RandomImage) Manifest(config core.Descriptor, layers []core.Descriptor) ([]byte, string, error) {
	data, err := core.BuildManifest(config, layers)
	return data, core.MediaTypeOCIManifest, err
}

// RandomBlob is the content of one random layer. Each Open streams the same
// bytes again, without keeping them in memory (IMG-3).
type RandomBlob struct {
	size int64
	seed [32]byte
}

var _ core.BlobSource = (*RandomBlob)(nil)

// Size is the layer size in bytes.
func (b *RandomBlob) Size() int64 { return b.size }

// MediaType is the OCI layer media type.
func (b *RandomBlob) MediaType() string { return core.MediaTypeOCILayer }

// Open returns a reader for the layer content.
func (b *RandomBlob) Open() (io.ReadCloser, error) {
	return io.NopCloser(io.LimitReader(rand.NewChaCha8(b.seed), b.size)), nil
}
