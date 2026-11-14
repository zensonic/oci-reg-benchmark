// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package imageset

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
)

// Reference is a parsed image reference.
type Reference struct {
	// Host is the registry host, for example "registry-1.docker.io".
	Host string
	// Repository is the repository path, for example "library/nginx".
	Repository string
	// Reference is a tag or digest.
	Reference string
}

// ParseReference parses references such as "nginx:1.27",
// "ghcr.io/org/app@sha256:..." and "harbor.local:8443/proj/app:v1". Docker
// Hub short names get the "library/" prefix. A digest wins over a tag.
func ParseReference(ref string) (Reference, error) {
	rest, digest, _ := strings.Cut(ref, "@")

	host := "registry-1.docker.io"
	if first, after, found := strings.Cut(rest, "/"); found &&
		(strings.ContainsAny(first, ".:") || first == "localhost") {
		host, rest = first, after
	}
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
	}

	tag := "latest"
	if colon := strings.LastIndex(rest, ":"); colon > strings.LastIndex(rest, "/") {
		rest, tag = rest[:colon], rest[colon+1:]
	}
	if rest == "" {
		return Reference{}, fmt.Errorf("invalid image reference %q", ref)
	}
	if host == "registry-1.docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}

	reference := tag
	if digest != "" {
		reference = digest
	}
	return Reference{Host: host, Repository: rest, Reference: reference}, nil
}

// ClientForHost returns a client for a source registry host.
type ClientForHost func(host string) (*core.Client, error)

// StaticSet is a downloaded set of real images (section 6.2).
type StaticSet struct {
	Images []*StaticImage
}

// TotalSize is the size of all images' blobs.
func (s *StaticSet) TotalSize() int64 {
	var total int64
	for _, img := range s.Images {
		total += img.Parsed.TotalSize()
	}
	return total
}

// Resolved returns the images with the digests they resolved to (IMG-8).
func (s *StaticSet) Resolved() []orbv1.ResolvedImage {
	out := make([]orbv1.ResolvedImage, len(s.Images))
	for i, img := range s.Images {
		out[i] = orbv1.ResolvedImage{Reference: img.Reference, Digest: img.Digest, Size: img.Parsed.TotalSize()}
	}
	return out
}

// StaticImage is one downloaded image. It implements core.Image by streaming
// blobs from disk and returning the original manifest.
type StaticImage struct {
	// Reference is the image reference from the spec.
	Reference string
	// Digest is the digest of the manifest that is pushed (the platform
	// manifest for multi-platform images).
	Digest      string
	RawManifest []byte
	MediaType   string
	Parsed      *core.Manifest

	dir string
}

var _ core.Image = (*StaticImage)(nil)

// DownloadStatic downloads every image of the static set into dir before
// measuring starts (IMG-6). The total size is checked against the storage
// limit before any blob is downloaded (IMG-9).
func DownloadStatic(ctx context.Context, spec *orbv1.StaticImageSet, dir string, clientFor ClientForHost) (*StaticSet, error) {
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o750); err != nil {
		return nil, fmt.Errorf("creating static image storage: %w", err)
	}

	type pending struct {
		image  *StaticImage
		client *core.Client
		repo   string
	}
	var images []pending
	var total int64

	for _, ref := range spec.Images {
		parsed, err := ParseReference(ref)
		if err != nil {
			return nil, err
		}
		client, err := clientFor(parsed.Host)
		if err != nil {
			return nil, err
		}
		img, err := resolveManifest(ctx, client, parsed, spec.Platform)
		if err != nil {
			return nil, fmt.Errorf("resolving static image %s: %w", ref, err)
		}
		img.Reference, img.dir = ref, dir
		total += img.Parsed.TotalSize()
		images = append(images, pending{image: img, client: client, repo: parsed.Repository})
	}

	if spec.StorageLimit != nil && total > spec.StorageLimit.Value() {
		return nil, fmt.Errorf("the static image set needs %d bytes, more than the storage limit of %s (IMG-9)",
			total, spec.StorageLimit.String())
	}

	set := &StaticSet{}
	for _, p := range images {
		for _, blob := range p.image.Parsed.Blobs() {
			if err := downloadBlob(ctx, p.client, p.repo, blob, dir); err != nil {
				return nil, fmt.Errorf("downloading %s: %w", p.image.Reference, err)
			}
		}
		set.Images = append(set.Images, p.image)
	}
	return set, nil
}

// resolveManifest fetches the manifest, picking the platform from an index.
func resolveManifest(ctx context.Context, client *core.Client, ref Reference, platform string) (*StaticImage, error) {
	result, err := client.GetManifest(ctx, core.StartTiming(), ref.Repository, ref.Reference)
	if err != nil {
		return nil, err
	}
	if result.Manifest.IsIndex() {
		chosen, err := result.Manifest.SelectPlatform(platform)
		if err != nil {
			return nil, err
		}
		result, err = client.GetManifest(ctx, core.StartTiming(), ref.Repository, chosen.Digest)
		if err != nil {
			return nil, err
		}
	}
	return &StaticImage{
		Digest:      result.Digest,
		RawManifest: result.Raw,
		MediaType:   result.MediaType,
		Parsed:      result.Manifest,
	}, nil
}

func downloadBlob(ctx context.Context, client *core.Client, repo string, blob core.Descriptor, dir string) error {
	path := blobPath(dir, blob.Digest)
	if info, err := os.Stat(path); err == nil && info.Size() == blob.Size {
		return nil // already downloaded, for example a layer shared by two images
	}

	tmp := path + ".partial"
	file, err := os.Create(tmp) //nolint:gosec // the path is built from a digest in our own directory
	if err != nil {
		return err
	}
	_, err = client.DownloadBlob(ctx, core.StartTiming(), repo, blob, file, true)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func blobPath(dir, digest string) string {
	return filepath.Join(dir, "blobs", strings.ReplaceAll(digest, ":", "-"))
}

// Layers returns the layer content, streamed from disk.
func (i *StaticImage) Layers() []core.BlobSource {
	out := make([]core.BlobSource, len(i.Parsed.Layers))
	for n, l := range i.Parsed.Layers {
		out[n] = i.Blob(l)
	}
	return out
}

// Config returns the original config blob.
func (i *StaticImage) Config([]core.Descriptor) ([]byte, string, error) {
	data, err := os.ReadFile(blobPath(i.dir, i.Parsed.Config.Digest))
	return data, i.Parsed.Config.MediaType, err
}

// Manifest returns the original manifest bytes.
func (i *StaticImage) Manifest(core.Descriptor, []core.Descriptor) ([]byte, string, error) {
	return i.RawManifest, i.MediaType, nil
}

// Blob returns the content of one of the image's blobs.
func (i *StaticImage) Blob(desc core.Descriptor) core.BlobSource {
	return fileBlob{path: blobPath(i.dir, desc.Digest), desc: desc}
}

type fileBlob struct {
	path string
	desc core.Descriptor
}

func (b fileBlob) Size() int64       { return b.desc.Size }
func (b fileBlob) MediaType() string { return b.desc.MediaType }
func (b fileBlob) Open() (io.ReadCloser, error) {
	return os.Open(b.path)
}
