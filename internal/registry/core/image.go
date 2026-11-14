// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"io"
	"time"
)

// Image is an image that can be pushed: layers, then a config, then a
// manifest that references them.
type Image interface {
	// Layers returns the layer content in order.
	Layers() []BlobSource

	// Config returns the config blob for the uploaded layers.
	Config(layers []Descriptor) (data []byte, mediaType string, err error)

	// Manifest returns the manifest that references the config and layers.
	Manifest(config Descriptor, layers []Descriptor) (data []byte, mediaType string, err error)
}

// PushResult is the outcome of pushing an image.
type PushResult struct {
	ManifestDigest string
	Manifest       []byte
	MediaType      string
	Config         Descriptor
	Layers         []Descriptor

	// FirstChunkAck is when the registry acknowledged the first chunk of the
	// first blob: the end of push latency (LAT-3).
	FirstChunkAck time.Time

	// Bytes is the blob data sent.
	Bytes int64

	// TransferTime is the sum of the blobs' first-to-last-byte times (THR-1).
	TransferTime time.Duration
}

// PushImage uploads all layers, the config and the manifest (OP-2).
func (c *Client) PushImage(ctx context.Context, timing *Timing, repository, tag string, img Image, chunkSize int64) (*PushResult, error) {
	result := &PushResult{}
	record := func(upload *UploadResult) {
		if result.FirstChunkAck.IsZero() && !upload.FirstChunkAck.IsZero() {
			result.FirstChunkAck = upload.FirstChunkAck
		}
		result.Bytes += upload.Descriptor.Size
		result.TransferTime += upload.TransferEnd.Sub(upload.TransferStart)
	}

	for _, layer := range img.Layers() {
		upload, err := c.UploadBlob(ctx, timing, repository, layer, chunkSize)
		if err != nil {
			return nil, err
		}
		record(upload)
		result.Layers = append(result.Layers, upload.Descriptor)
	}

	configData, configType, err := img.Config(result.Layers)
	if err != nil {
		return nil, err
	}
	upload, err := c.UploadBlob(ctx, timing, repository, BytesBlob(configData, configType), chunkSize)
	if err != nil {
		return nil, err
	}
	record(upload)
	result.Config = upload.Descriptor

	manifest, mediaType, err := img.Manifest(result.Config, result.Layers)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	digest, err := c.PutManifest(ctx, timing, repository, tag, mediaType, manifest)
	if err != nil {
		return nil, err
	}
	timing.Add(PhaseManifest, time.Since(started))

	result.ManifestDigest = digest
	result.Manifest = manifest
	result.MediaType = mediaType
	return result, nil
}

// PullResult is the outcome of pulling an image.
type PullResult struct {
	Manifest *ManifestResult

	// FirstBlobByte is when the first byte of the first blob body arrived:
	// the end of pull latency (LAT-2).
	FirstBlobByte time.Time

	// Bytes is the blob data received.
	Bytes int64

	// TransferTime is the sum of the blobs' first-to-last-byte times (THR-1).
	TransferTime time.Duration
}

// PullImage fetches the manifest and then every blob, config first, the same
// order a container runtime uses (OP-1). If the reference points to an
// index, the manifest for platform is used.
func (c *Client) PullImage(ctx context.Context, timing *Timing, repository, reference, platform string, verify bool) (*PullResult, error) {
	manifest, err := c.timedGetManifest(ctx, timing, repository, reference)
	if err != nil {
		return nil, err
	}
	if manifest.Manifest.IsIndex() {
		chosen, err := manifest.Manifest.SelectPlatform(platform)
		if err != nil {
			return nil, err
		}
		manifest, err = c.timedGetManifest(ctx, timing, repository, chosen.Digest)
		if err != nil {
			return nil, err
		}
	}

	result := &PullResult{Manifest: manifest}
	for _, blob := range manifest.Manifest.Blobs() {
		download, err := c.DownloadBlob(ctx, timing, repository, blob, nil, verify)
		if err != nil {
			return nil, err
		}
		if result.FirstBlobByte.IsZero() {
			result.FirstBlobByte = download.FirstByte
		}
		result.Bytes += download.Bytes
		result.TransferTime += download.LastByte.Sub(download.FirstByte)
	}
	return result, nil
}

func (c *Client) timedGetManifest(ctx context.Context, timing *Timing, repository, reference string) (*ManifestResult, error) {
	started := time.Now()
	manifest, err := c.GetManifest(ctx, timing, repository, reference)
	timing.Add(PhaseManifest, time.Since(started))
	return manifest, err
}

// BytesBlob returns a BlobSource for data held in memory.
func BytesBlob(data []byte, mediaType string) BlobSource {
	return bytesBlob{data: data, mediaType: mediaType}
}

type bytesBlob struct {
	data      []byte
	mediaType string
}

func (b bytesBlob) Size() int64       { return int64(len(b.data)) }
func (b bytesBlob) MediaType() string { return b.mediaType }
func (b bytesBlob) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b.data)), nil
}
