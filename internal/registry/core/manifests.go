// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Media types ORB reads and writes.
const (
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeOCIConfig      = "application/vnd.oci.image.config.v1+json"
	MediaTypeOCILayer       = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// manifestAccept is the Accept header for manifest requests.
var manifestAccept = strings.Join([]string{
	MediaTypeOCIManifest, MediaTypeOCIIndex, MediaTypeDockerManifest, MediaTypeDockerList,
}, ", ")

// maxManifestSize protects against huge manifest responses.
const maxManifestSize = 4 << 20

// Descriptor points to a blob or manifest.
type Descriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Platform  *Platform `json:"platform,omitempty"`
}

// Platform is the platform of a manifest in an index.
type Platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

// Manifest is an image manifest or an index. Only the fields ORB needs are
// decoded.
type Manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType,omitempty"`
	Config        Descriptor   `json:"config,omitzero"`
	Layers        []Descriptor `json:"layers,omitempty"`
	Manifests     []Descriptor `json:"manifests,omitempty"`
}

// IsIndex reports whether the manifest is an index (multi-platform list).
func (m *Manifest) IsIndex() bool {
	return m.MediaType == MediaTypeOCIIndex || m.MediaType == MediaTypeDockerList || len(m.Manifests) > 0
}

// Blobs returns the config and all layers, in pull order.
func (m *Manifest) Blobs() []Descriptor {
	return append([]Descriptor{m.Config}, m.Layers...)
}

// TotalSize is the size of the config and all layers.
func (m *Manifest) TotalSize() int64 {
	total := m.Config.Size
	for _, l := range m.Layers {
		total += l.Size
	}
	return total
}

// ParseManifest decodes manifest JSON. The media type from the response
// header is used if the manifest does not include one.
func ParseManifest(raw []byte, headerMediaType string) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	if m.MediaType == "" {
		m.MediaType = headerMediaType
	}
	return &m, nil
}

// SelectPlatform picks the manifest for a platform such as "linux/amd64"
// from an index.
func (m *Manifest) SelectPlatform(platform string) (Descriptor, error) {
	wantOS, wantArch, _ := strings.Cut(platform, "/")
	wantArch, wantVariant, _ := strings.Cut(wantArch, "/")
	for _, d := range m.Manifests {
		if d.Platform == nil {
			continue
		}
		if d.Platform.OS == wantOS && d.Platform.Architecture == wantArch &&
			(wantVariant == "" || d.Platform.Variant == wantVariant) {
			return d, nil
		}
	}
	return Descriptor{}, fmt.Errorf("the index has no manifest for platform %s", platform)
}

// BuildManifest builds an OCI image manifest for a config and layers.
func BuildManifest(config Descriptor, layers []Descriptor) ([]byte, error) {
	m := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIManifest,
		Config:        config,
		Layers:        layers,
	}
	return json.Marshal(m)
}

// Digest returns the sha256 digest of data in "sha256:<hex>" form.
func Digest(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// ManifestResult is a fetched manifest.
type ManifestResult struct {
	Raw       []byte
	MediaType string
	Digest    string
	Manifest  *Manifest
	// FirstByte is when the first byte of the response arrived.
	FirstByte time.Time
}

// GetManifest fetches a manifest by tag or digest.
func (c *Client) GetManifest(ctx context.Context, timing *Timing, repository, reference string) (*ManifestResult, error) {
	resp, err := c.do(ctx, timing, request{
		method: http.MethodGet,
		path:   repoPath(repository, "manifests/"+reference),
		header: http.Header{"Accept": {manifestAccept}},
		scopes: []string{RepositoryScope(repository, "pull")},
		expect: []int{http.StatusOK},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	if len(raw) > maxManifestSize {
		return nil, errors.New("manifest is larger than 4 MiB")
	}

	mediaType := resp.Header.Get("Content-Type")
	m, err := ParseManifest(raw, mediaType)
	if err != nil {
		return nil, err
	}
	return &ManifestResult{
		Raw:       raw,
		MediaType: m.MediaType,
		Digest:    Digest(raw),
		Manifest:  m,
		FirstByte: resp.trace.FirstByte(),
	}, nil
}

// HeadResult is the answer to a HEAD request.
type HeadResult struct {
	Exists    bool
	Digest    string
	Size      int64
	FirstByte time.Time
}

// HeadManifest checks whether a manifest exists (OP-3).
func (c *Client) HeadManifest(ctx context.Context, timing *Timing, repository, reference string) (*HeadResult, error) {
	return c.head(ctx, timing, repository, repoPath(repository, "manifests/"+reference), http.Header{"Accept": {manifestAccept}})
}

// HeadBlob checks whether a blob exists in a repository (OP-4).
func (c *Client) HeadBlob(ctx context.Context, timing *Timing, repository, digest string) (*HeadResult, error) {
	return c.head(ctx, timing, repository, repoPath(repository, "blobs/"+digest), nil)
}

func (c *Client) head(ctx context.Context, timing *Timing, repository, path string, header http.Header) (*HeadResult, error) {
	resp, err := c.do(ctx, timing, request{
		method: http.MethodHead,
		path:   path,
		header: header,
		scopes: []string{RepositoryScope(repository, "pull")},
		expect: []int{http.StatusOK, http.StatusNotFound},
	})
	if err != nil {
		return nil, err
	}
	drainAndClose(resp.Response)
	return &HeadResult{
		Exists:    resp.StatusCode == http.StatusOK,
		Digest:    resp.Header.Get("Docker-Content-Digest"),
		Size:      resp.ContentLength,
		FirstByte: resp.trace.FirstByte(),
	}, nil
}

// PutManifest uploads a manifest under a tag or digest. It is used for pushes
// and for tagging an existing manifest (OP-5). It returns when the registry
// has answered, which is the end of tag latency (LAT-5).
func (c *Client) PutManifest(ctx context.Context, timing *Timing, repository, reference, mediaType string, raw []byte) (string, error) {
	resp, err := c.do(ctx, timing, request{
		method: http.MethodPut,
		path:   repoPath(repository, "manifests/"+reference),
		header: http.Header{"Content-Type": {mediaType}},
		body:   raw,
		scopes: []string{RepositoryScope(repository, "pull", "push")},
		expect: []int{http.StatusCreated, http.StatusOK},
	})
	if err != nil {
		return "", err
	}
	drainAndClose(resp.Response)

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		digest = Digest(raw)
	}
	return digest, nil
}

// DeleteManifest deletes a manifest by digest. Registries that do not allow
// deletion answer 405, which is returned as ErrUnsupported.
func (c *Client) DeleteManifest(ctx context.Context, repository, digest string) error {
	resp, err := c.do(ctx, StartTiming(), request{
		method: http.MethodDelete,
		path:   repoPath(repository, "manifests/"+digest),
		scopes: []string{RepositoryScope(repository, "pull", "push", "delete")},
		expect: []int{http.StatusAccepted, http.StatusOK, http.StatusNotFound},
	})
	if IsStatus(err, http.StatusMethodNotAllowed) || IsStatus(err, http.StatusUnsupportedMediaType) {
		return ErrUnsupported
	}
	if err != nil {
		return err
	}
	drainAndClose(resp.Response)
	return nil
}

// Ping calls /v2/ to check that the registry is reachable and that the
// credentials work.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, StartTiming(), request{
		method: http.MethodGet,
		path:   "/v2/",
		expect: []int{http.StatusOK},
	})
	if err != nil {
		return err
	}
	drainAndClose(resp.Response)
	return nil
}
