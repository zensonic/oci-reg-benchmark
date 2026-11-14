// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"time"
)

// BlobSource is blob content that can be uploaded.
type BlobSource interface {
	// Size is the blob size in bytes.
	Size() int64
	// MediaType is the media type used in the manifest.
	MediaType() string
	// Open returns a reader for the blob content.
	Open() (io.ReadCloser, error)
}

// UploadResult is the outcome of uploading one blob.
type UploadResult struct {
	Descriptor Descriptor

	// FirstChunkAck is when the registry acknowledged the first chunk
	// (the end of push latency, LAT-3). Zero for empty blobs.
	FirstChunkAck time.Time

	// TransferStart is when the first byte of data was sent, and
	// TransferEnd when the registry confirmed the committed blob.
	TransferStart time.Time
	TransferEnd   time.Time
}

// StartUpload opens an upload session (the POST in OP-2) and returns its
// Location.
func (c *Client) StartUpload(ctx context.Context, timing *Timing, repository string) (*url.URL, error) {
	started := time.Now()
	resp, err := c.do(ctx, timing, request{
		method: http.MethodPost,
		path:   repoPath(repository, "blobs/uploads/"),
		scopes: []string{RepositoryScope(repository, "pull", "push")},
		expect: []int{http.StatusAccepted},
	})
	if err != nil {
		return nil, err
	}
	drainAndClose(resp.Response)
	timing.Add(PhaseUploadSession, time.Since(started))
	return uploadLocation(resp.Response)
}

// MountResult is the outcome of a cross-repository mount request.
type MountResult struct {
	// Mounted is true if the registry mounted the blob (201 Created).
	Mounted bool
	// UploadURL is set on a mount miss (202 Accepted): the registry started a
	// normal upload session instead (DEDUP-6).
	UploadURL *url.URL
	// FirstByte is when the registry's answer arrived.
	FirstByte time.Time
}

// MountBlob asks the registry to mount a blob from another repository
// (DEDUP-4, crossRepoMount).
func (c *Client) MountBlob(ctx context.Context, timing *Timing, repository, digest, fromRepository string) (*MountResult, error) {
	resp, err := c.do(ctx, timing, request{
		method: http.MethodPost,
		path:   repoPath(repository, "blobs/uploads/"),
		query:  url.Values{"mount": {digest}, "from": {fromRepository}},
		scopes: []string{
			RepositoryScope(repository, "pull", "push"),
			RepositoryScope(fromRepository, "pull"),
		},
		expect: []int{http.StatusCreated, http.StatusAccepted},
	})
	if err != nil {
		return nil, err
	}
	drainAndClose(resp.Response)

	result := &MountResult{Mounted: resp.StatusCode == http.StatusCreated, FirstByte: resp.trace.FirstByte()}
	if !result.Mounted {
		result.UploadURL, err = uploadLocation(resp.Response)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// UploadBlob uploads a blob: it opens a session, sends the data in chunks and
// commits it (OP-2).
func (c *Client) UploadBlob(ctx context.Context, timing *Timing, repository string, src BlobSource, chunkSize int64) (*UploadResult, error) {
	location, err := c.StartUpload(ctx, timing, repository)
	if err != nil {
		return nil, err
	}
	return c.UploadToSession(ctx, timing, repository, location, src, chunkSize)
}

// UploadToSession sends blob data to an open upload session in chunks and
// commits it. The digest is computed while the data streams, so no local
// copy of the blob is needed (IMG-3).
func (c *Client) UploadToSession(ctx context.Context, timing *Timing, repository string, location *url.URL, src BlobSource, chunkSize int64) (*UploadResult, error) {
	if chunkSize <= 0 {
		return nil, errors.New("chunk size must be positive")
	}
	reader, err := src.Open()
	if err != nil {
		return nil, fmt.Errorf("opening blob data: %w", err)
	}
	defer reader.Close()

	hasher := sha256.New()
	result := &UploadResult{}
	size := src.Size()
	scopes := []string{RepositoryScope(repository, "pull", "push")}

	for offset := int64(0); offset < size; {
		length := min(chunkSize, size-offset)
		chunkStart := time.Now()
		if result.TransferStart.IsZero() {
			result.TransferStart = chunkStart
		}

		location, err = c.sendChunk(ctx, timing, location, reader, hasher, offset, length, scopes)
		if err != nil {
			return nil, err
		}
		if result.FirstChunkAck.IsZero() {
			result.FirstChunkAck = time.Now()
			timing.Add(PhaseFirstChunk, result.FirstChunkAck.Sub(chunkStart))
		}
		offset += length
	}

	digest := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	if err := c.commitUpload(ctx, timing, location, digest, scopes); err != nil {
		return nil, err
	}
	result.TransferEnd = time.Now()
	if result.TransferStart.IsZero() {
		result.TransferStart = result.TransferEnd
	}
	result.Descriptor = Descriptor{MediaType: src.MediaType(), Digest: digest, Size: size}
	return result, nil
}

// sendChunk sends one PATCH with a part of the blob and returns the next
// Location.
func (c *Client) sendChunk(ctx context.Context, timing *Timing, location *url.URL, reader io.Reader, hasher hash.Hash, offset, length int64, scopes []string) (*url.URL, error) {
	body := io.TeeReader(io.LimitReader(reader, length), hasher)
	resp, err := c.do(ctx, timing, request{
		method: http.MethodPatch,
		url:    location,
		header: http.Header{
			"Content-Type":  {"application/octet-stream"},
			"Content-Range": {fmt.Sprintf("%d-%d", offset, offset+length-1)},
		},
		stream:       body,
		streamLength: length,
		scopes:       scopes,
		expect:       []int{http.StatusAccepted, http.StatusNoContent},
	})
	if err != nil {
		return nil, err
	}
	drainAndClose(resp.Response)
	return uploadLocation(resp.Response)
}

// commitUpload finishes an upload session with the final PUT.
func (c *Client) commitUpload(ctx context.Context, timing *Timing, location *url.URL, digest string, scopes []string) error {
	target := *location
	query := target.Query()
	query.Set("digest", digest)
	target.RawQuery = query.Encode()

	started := time.Now()
	resp, err := c.do(ctx, timing, request{
		method: http.MethodPut,
		url:    &target,
		header: http.Header{"Content-Type": {"application/octet-stream"}},
		body:   []byte{},
		scopes: scopes,
		expect: []int{http.StatusCreated, http.StatusNoContent},
	})
	if err != nil {
		return err
	}
	drainAndClose(resp.Response)
	timing.Add(PhaseCommit, time.Since(started))
	return nil
}

// uploadLocation returns the absolute upload URL from a response.
func uploadLocation(resp *http.Response) (*url.URL, error) {
	location, err := resp.Location()
	if err != nil {
		return nil, fmt.Errorf("the registry sent no valid upload Location: %w", err)
	}
	return location, nil
}

// DownloadResult is the outcome of downloading one blob.
type DownloadResult struct {
	Bytes int64
	// FirstByte is when the first byte of the blob body arrived (LAT-2).
	FirstByte time.Time
	// LastByte is when the last byte arrived.
	LastByte time.Time
}

// DownloadBlob reads a blob in full. The data goes to sink, or is thrown away
// if sink is nil (OP-10). If verify is true, the content is checked against
// its digest.
func (c *Client) DownloadBlob(ctx context.Context, timing *Timing, repository string, blob Descriptor, sink io.Writer, verify bool) (*DownloadResult, error) {
	started := time.Now()
	resp, err := c.do(ctx, timing, request{
		method: http.MethodGet,
		path:   repoPath(repository, "blobs/"+blob.Digest),
		scopes: []string{RepositoryScope(repository, "pull")},
		expect: []int{http.StatusOK},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Registries often redirect blob downloads to object storage. The HTTP
	// client follows redirects, so the first body byte is the moment we care
	// about, not the first byte of a redirect response.
	body := &firstByteReader{reader: resp.Body}
	writers := []io.Writer{}
	if sink != nil {
		writers = append(writers, sink)
	}
	var hasher hash.Hash
	if verify {
		hasher = sha256.New()
		writers = append(writers, hasher)
	}
	if len(writers) == 0 {
		writers = append(writers, io.Discard)
	}

	n, err := io.Copy(io.MultiWriter(writers...), body)
	if err != nil {
		return nil, fmt.Errorf("reading blob %s: %w", blob.Digest, err)
	}
	result := &DownloadResult{Bytes: n, FirstByte: body.firstByte, LastByte: time.Now()}
	if result.FirstByte.IsZero() {
		result.FirstByte = result.LastByte
	}
	timing.Add(PhaseTTFB, result.FirstByte.Sub(started))

	if verify {
		if got := fmt.Sprintf("sha256:%x", hasher.Sum(nil)); got != blob.Digest {
			return nil, fmt.Errorf("blob %s failed verification: content has digest %s", blob.Digest, got)
		}
	}
	return result, nil
}

// firstByteReader remembers when the first byte was read.
type firstByteReader struct {
	reader    io.Reader
	firstByte time.Time
}

func (r *firstByteReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.firstByte.IsZero() {
		r.firstByte = time.Now()
	}
	return n, err
}
