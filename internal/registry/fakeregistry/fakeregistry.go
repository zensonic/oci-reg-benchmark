// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package fakeregistry is a small in-memory OCI registry for tests. It
// implements the parts of the OCI Distribution API that ORB uses: blob
// uploads (chunked, with cross-repository mounts), downloads, HEAD,
// manifests, tag listing and catalog with pagination, and optionally the
// bearer token flow.
package fakeregistry

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Options change how the fake registry behaves.
type Options struct {
	// Username and Password turn on the bearer token flow when set.
	Username string
	Password string

	// RefuseMounts makes every mount request a mount miss (202).
	RefuseMounts bool

	// RefuseDeletes makes manifest deletion answer 405.
	RefuseDeletes bool
}

// Registry is a running fake registry.
type Registry struct {
	*httptest.Server
	opts Options

	mu        sync.Mutex
	blobs     map[string][]byte          // digest -> content
	links     map[string]map[string]bool // repository -> digests linked to it
	manifests map[string]map[string]manifestEntry
	uploads   map[string]*upload
	nextID    int

	// Requests counts requests by "METHOD kind", for example "PATCH upload".
	Requests map[string]int
}

type manifestEntry struct {
	data      []byte
	mediaType string
}

type upload struct {
	repository string
	data       []byte
}

// Start starts a fake registry. Call Close when done.
func Start(opts Options) *Registry {
	r := &Registry{
		opts:      opts,
		blobs:     map[string][]byte{},
		links:     map[string]map[string]bool{},
		manifests: map[string]map[string]manifestEntry{},
		uploads:   map[string]*upload{},
		Requests:  map[string]int{},
	}
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	return r
}

const fakeToken = "fake-token"

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/token" {
		r.serveToken(w, req)
		return
	}
	if r.opts.Username != "" && req.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake"`, r.URL))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	switch {
	case req.URL.Path == "/v2/":
		w.WriteHeader(http.StatusOK)
	case path == "_catalog":
		r.count(req, "catalog")
		r.serveCatalog(w, req)
	case strings.Contains(path, "/blobs/uploads/"):
		r.count(req, "upload")
		repo, id, _ := strings.Cut(path, "/blobs/uploads/")
		r.serveUpload(w, req, repo, id)
	case strings.Contains(path, "/blobs/"):
		r.count(req, "blob")
		repo, digest, _ := strings.Cut(path, "/blobs/")
		r.serveBlob(w, req, repo, digest)
	case strings.Contains(path, "/manifests/"):
		r.count(req, "manifest")
		repo, ref, _ := strings.Cut(path, "/manifests/")
		r.serveManifest(w, req, repo, ref)
	case strings.HasSuffix(path, "/tags/list"):
		r.count(req, "tags")
		r.serveTags(w, req, strings.TrimSuffix(path, "/tags/list"))
	default:
		http.NotFound(w, req)
	}
}

func (r *Registry) count(req *http.Request, kind string) {
	r.mu.Lock()
	r.Requests[req.Method+" "+kind]++
	r.mu.Unlock()
}

func (r *Registry) serveToken(w http.ResponseWriter, req *http.Request) {
	user, pass, ok := req.BasicAuth()
	if !ok || user != r.opts.Username || pass != r.opts.Password {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"token": fakeToken, "expires_in": 300})
}

func (r *Registry) serveUpload(w http.ResponseWriter, req *http.Request, repo, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch req.Method {
	case http.MethodPost:
		if digest := req.URL.Query().Get("mount"); digest != "" && !r.opts.RefuseMounts {
			from := req.URL.Query().Get("from")
			if r.links[from][digest] {
				r.link(repo, digest)
				w.Header().Set("Docker-Content-Digest", digest)
				w.WriteHeader(http.StatusCreated)
				return
			}
		}
		r.nextID++
		newID := strconv.Itoa(r.nextID)
		r.uploads[newID] = &upload{repository: repo}
		w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/"+newID)
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPatch:
		up, ok := r.uploads[id]
		if !ok {
			http.Error(w, "unknown upload", http.StatusNotFound)
			return
		}
		data, _ := io.ReadAll(req.Body)
		up.data = append(up.data, data...)
		w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/"+id)
		w.Header().Set("Range", fmt.Sprintf("0-%d", len(up.data)-1))
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPut:
		up, ok := r.uploads[id]
		if !ok {
			http.Error(w, "unknown upload", http.StatusNotFound)
			return
		}
		data, _ := io.ReadAll(req.Body)
		up.data = append(up.data, data...)
		want := req.URL.Query().Get("digest")
		if got := digestOf(up.data); got != want {
			http.Error(w, "digest mismatch: got "+got, http.StatusBadRequest)
			return
		}
		r.blobs[want] = up.data
		r.link(repo, want)
		delete(r.uploads, id)
		w.Header().Set("Docker-Content-Digest", want)
		w.WriteHeader(http.StatusCreated)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *Registry) link(repo, digest string) {
	if r.links[repo] == nil {
		r.links[repo] = map[string]bool{}
	}
	r.links[repo][digest] = true
}

func (r *Registry) serveBlob(w http.ResponseWriter, req *http.Request, repo, digest string) {
	r.mu.Lock()
	data, ok := r.blobs[digest]
	linked := r.links[repo][digest]
	r.mu.Unlock()

	if !ok || !linked {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Docker-Content-Digest", digest)
	if req.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func (r *Registry) serveManifest(w http.ResponseWriter, req *http.Request, repo, ref string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch req.Method {
	case http.MethodPut:
		data, _ := io.ReadAll(req.Body)
		entry := manifestEntry{data: data, mediaType: req.Header.Get("Content-Type")}
		if r.manifests[repo] == nil {
			r.manifests[repo] = map[string]manifestEntry{}
		}
		digest := digestOf(data)
		r.manifests[repo][ref] = entry
		r.manifests[repo][digest] = entry
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)

	case http.MethodGet, http.MethodHead:
		entry, ok := r.manifests[repo][ref]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", entry.mediaType)
		w.Header().Set("Docker-Content-Digest", digestOf(entry.data))
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.data)))
		if req.Method == http.MethodGet {
			_, _ = w.Write(entry.data)
		}

	case http.MethodDelete:
		if r.opts.RefuseDeletes {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		for key, entry := range r.manifests[repo] {
			if digestOf(entry.data) == ref {
				delete(r.manifests[repo], key)
			}
		}
		if len(r.manifests[repo]) == 0 {
			delete(r.manifests, repo)
		}
		w.WriteHeader(http.StatusAccepted)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *Registry) serveTags(w http.ResponseWriter, req *http.Request, repo string) {
	r.mu.Lock()
	var tags []string
	for ref := range r.manifests[repo] {
		if !strings.HasPrefix(ref, "sha256:") {
			tags = append(tags, ref)
		}
	}
	r.mu.Unlock()
	if tags == nil {
		http.NotFound(w, req)
		return
	}
	writePage(w, req, "/v2/"+repo+"/tags/list", "tags", map[string]any{"name": repo}, tags)
}

func (r *Registry) serveCatalog(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	repos := make([]string, 0, len(r.manifests))
	for repo := range r.manifests {
		repos = append(repos, repo)
	}
	r.mu.Unlock()
	writePage(w, req, "/v2/_catalog", "repositories", map[string]any{}, repos)
}

// writePage writes one page of a sorted listing with a Link header to the
// next page.
func writePage(w http.ResponseWriter, req *http.Request, path, field string, body map[string]any, names []string) {
	sort.Strings(names)
	last := req.URL.Query().Get("last")
	start := sort.SearchStrings(names, last)
	if last != "" && start < len(names) && names[start] == last {
		start++
	}
	n, err := strconv.Atoi(req.URL.Query().Get("n"))
	if err != nil || n <= 0 {
		n = len(names)
	}
	end := min(start+n, len(names))
	page := names[start:end]
	if end < len(names) {
		w.Header().Set("Link", fmt.Sprintf(`<%s?last=%s&n=%d>; rel="next"`, path, page[len(page)-1], n))
	}
	body[field] = page
	_ = json.NewEncoder(w).Encode(body)
}

// HasManifest reports whether the repository has a manifest with the reference.
func (r *Registry) HasManifest(repo, ref string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.manifests[repo][ref]
	return ok
}

// RequestCount returns how many requests of a kind were made, for example
// RequestCount("PATCH", "upload").
func (r *Registry) RequestCount(method, kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Requests[method+" "+kind]
}

func digestOf(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}
