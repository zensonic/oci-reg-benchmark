// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/fakeregistry"
)

// testImage is a small image with random layers.
type testImage struct{ layers []core.BlobSource }

func newTestImage(t *testing.T, sizes ...int) *testImage {
	t.Helper()
	img := &testImage{}
	for _, size := range sizes {
		data := make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		img.layers = append(img.layers, core.BytesBlob(data, core.MediaTypeOCILayer))
	}
	return img
}

func (i *testImage) Layers() []core.BlobSource { return i.layers }
func (i *testImage) Config(layers []core.Descriptor) ([]byte, string, error) {
	data, err := json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "layers": len(layers)})
	return data, core.MediaTypeOCIConfig, err
}
func (i *testImage) Manifest(config core.Descriptor, layers []core.Descriptor) ([]byte, string, error) {
	data, err := core.BuildManifest(config, layers)
	return data, core.MediaTypeOCIManifest, err
}

func newClient(t *testing.T, reg *fakeregistry.Registry, creds *core.Credentials) *core.Client {
	t.Helper()
	c, err := core.New(core.Config{BaseURL: reg.URL, Credentials: creds})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestPushThenPullWithBearerAuth(t *testing.T) {
	reg := fakeregistry.Start(fakeregistry.Options{Username: "bench", Password: "secret"})
	defer reg.Close()
	client := newClient(t, reg, &core.Credentials{Username: "bench", Password: "secret"})
	ctx := context.Background()

	// 2.5 chunks of 1 KiB, plus a second layer, to exercise chunking.
	img := newTestImage(t, 2560, 100)
	timing := core.StartTiming()
	pushed, err := client.PushImage(ctx, timing, "bench/app", "v1", img, 1024)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushed.FirstChunkAck.IsZero() || pushed.FirstChunkAck.Before(timing.Start) {
		t.Error("first chunk acknowledgement was not recorded")
	}
	if got := reg.RequestCount("PATCH", "upload"); got != 5 {
		t.Errorf("sent %d chunks, want 5 (3 for the first layer, 1 for the second, 1 for the config)", got)
	}
	if timing.Phases()[core.PhaseAuth] == 0 {
		t.Error("auth phase was not recorded")
	}

	pullTiming := core.StartTiming()
	pulled, err := client.PullImage(ctx, pullTiming, "bench/app", "v1", "linux/amd64", true)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pulled.Bytes != pushed.Bytes {
		t.Errorf("pulled %d bytes, pushed %d", pulled.Bytes, pushed.Bytes)
	}
	if pulled.FirstBlobByte.IsZero() {
		t.Error("first blob byte was not recorded")
	}
	if pulled.Manifest.Digest != pushed.ManifestDigest {
		t.Errorf("manifest digest %s, want %s", pulled.Manifest.Digest, pushed.ManifestDigest)
	}
}

func TestWrongCredentialsFail(t *testing.T) {
	reg := fakeregistry.Start(fakeregistry.Options{Username: "bench", Password: "secret"})
	defer reg.Close()
	client := newClient(t, reg, &core.Credentials{Username: "bench", Password: "wrong"})

	err := client.Ping(context.Background())
	if err == nil {
		t.Fatal("expected an error with wrong credentials")
	}
}

func TestHeadTagListAndCatalog(t *testing.T) {
	reg := fakeregistry.Start(fakeregistry.Options{})
	defer reg.Close()
	client := newClient(t, reg, nil)
	ctx := context.Background()

	pushed, err := client.PushImage(ctx, core.StartTiming(), "bench/app", "v1", newTestImage(t, 10), 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"a", "b", "c", "d"} {
		if _, err := client.PutManifest(ctx, core.StartTiming(), "bench/app", tag, pushed.MediaType, pushed.Manifest); err != nil {
			t.Fatal(err)
		}
	}

	head, err := client.HeadManifest(ctx, core.StartTiming(), "bench/app", "v1")
	if err != nil || !head.Exists || head.Digest != pushed.ManifestDigest {
		t.Fatalf("HEAD manifest: %+v, %v", head, err)
	}
	missing, err := client.HeadBlob(ctx, core.StartTiming(), "bench/app", "sha256:0000")
	if err != nil || missing.Exists {
		t.Fatalf("HEAD of a missing blob: %+v, %v", missing, err)
	}

	tags, err := client.ListTags(ctx, core.StartTiming(), "bench/app", 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if tags.Entries != 5 || tags.Pages != 3 {
		t.Errorf("tags: %d entries on %d pages, want 5 on 3", tags.Entries, tags.Pages)
	}

	catalog, err := client.Catalog(ctx, core.StartTiming(), 10, true)
	if err != nil || catalog.Entries != 1 {
		t.Fatalf("catalog: %+v, %v", catalog, err)
	}
}

func TestMountHitAndMiss(t *testing.T) {
	reg := fakeregistry.Start(fakeregistry.Options{})
	defer reg.Close()
	client := newClient(t, reg, nil)
	ctx := context.Background()

	pushed, err := client.PushImage(ctx, core.StartTiming(), "bench/source", "v1", newTestImage(t, 50), 1024)
	if err != nil {
		t.Fatal(err)
	}
	layer := pushed.Layers[0]

	hit, err := client.MountBlob(ctx, core.StartTiming(), "bench/target", layer.Digest, "bench/source")
	if err != nil || !hit.Mounted {
		t.Fatalf("mount from a repository that has the blob: %+v, %v", hit, err)
	}

	miss, err := client.MountBlob(ctx, core.StartTiming(), "bench/other", layer.Digest, "bench/nowhere")
	if err != nil || miss.Mounted || miss.UploadURL == nil {
		t.Fatalf("mount miss should start an upload: %+v, %v", miss, err)
	}
}

func TestDeleteRefusedIsUnsupported(t *testing.T) {
	reg := fakeregistry.Start(fakeregistry.Options{RefuseDeletes: true})
	defer reg.Close()
	client := newClient(t, reg, nil)

	err := client.DeleteManifest(context.Background(), "bench/app", "sha256:abc")
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

func TestClassifyError(t *testing.T) {
	cases := map[string]error{
		"http_503": &core.HTTPError{StatusCode: http.StatusServiceUnavailable},
		"timeout":  context.DeadlineExceeded,
		"canceled": context.Canceled,
		"other":    errors.New("something else"),
	}
	for want, err := range cases {
		if got := core.ClassifyError(err); got != want {
			t.Errorf("ClassifyError(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestParseDockerConfig(t *testing.T) {
	data := []byte(`{"auths":{"https://harbor.example.com":{"auth":"YmVuY2g6c2VjcmV0"}}}`)
	creds, err := core.ParseDockerConfig(data, "harbor.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil || creds.Username != "bench" || creds.Password != "secret" {
		t.Fatalf("got %+v", creds)
	}
	none, err := core.ParseDockerConfig(data, "other.example.com")
	if err != nil || none != nil {
		t.Fatalf("expected no credentials for another host, got %+v, %v", none, err)
	}
}
