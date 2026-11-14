// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package imageset

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"io"
	"net/url"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/fakeregistry"
)

func quantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

func readAll(t *testing.T, b core.BlobSource) []byte {
	t.Helper()
	r, err := b.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRandomImagesAreReproducibleAndUnique(t *testing.T) {
	spec := &orbv1.RandomImageSet{Layers: 2, LayerSize: orbv1.SizeDistribution{Distribution: "Fixed", Size: quantity("64Ki")}}
	a := NewRandom(spec, 42).Image(Key("load", "push", 0, 1, 1))
	again := NewRandom(spec, 42).Image(Key("load", "push", 0, 1, 1))
	other := NewRandom(spec, 42).Image(Key("load", "push", 0, 1, 2))

	first := readAll(t, a.Layers()[0])
	if len(first) != 64*1024 {
		t.Fatalf("layer has %d bytes, want 65536", len(first))
	}
	if sha256.Sum256(first) != sha256.Sum256(readAll(t, again.Layers()[0])) {
		t.Error("the same key gave different content")
	}
	if bytes.Equal(first, readAll(t, other.Layers()[0])) {
		t.Error("different keys gave the same content")
	}
	if bytes.Equal(first, readAll(t, a.Layers()[1])) {
		t.Error("two layers of one image have the same content")
	}
}

func TestRandomContentIsIncompressible(t *testing.T) {
	spec := &orbv1.RandomImageSet{Layers: 1, LayerSize: orbv1.SizeDistribution{Distribution: "Fixed", Size: quantity("256Ki")}}
	data := readAll(t, NewRandom(spec, 1).Image(1).Layers()[0])

	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	_, _ = w.Write(data)
	_ = w.Close()
	if compressed.Len() < len(data)*99/100 {
		t.Errorf("random data compressed from %d to %d bytes (IMG-2)", len(data), compressed.Len())
	}
}

func TestSizeDistributions(t *testing.T) {
	uniform := &orbv1.RandomImageSet{Layers: 20, LayerSize: orbv1.SizeDistribution{
		Distribution: "Uniform", Min: quantity("1Ki"), Max: quantity("4Ki")}}
	for _, size := range NewRandom(uniform, 7).Image(3).LayerSizes() {
		if size < 1024 || size > 4096 {
			t.Errorf("uniform size %d outside 1Ki-4Ki", size)
		}
	}

	list := &orbv1.RandomImageSet{Layers: 20, LayerSize: orbv1.SizeDistribution{
		Distribution: "List", Sizes: []resource.Quantity{resource.MustParse("1Ki"), resource.MustParse("3Ki")}}}
	for _, size := range NewRandom(list, 7).Image(3).LayerSizes() {
		if size != 1024 && size != 3072 {
			t.Errorf("list size %d not in the list", size)
		}
	}
}

func TestParseReference(t *testing.T) {
	cases := map[string]Reference{
		"nginx":                          {"registry-1.docker.io", "library/nginx", "latest"},
		"nginx:1.27":                     {"registry-1.docker.io", "library/nginx", "1.27"},
		"docker.io/org/app:v1":           {"registry-1.docker.io", "org/app", "v1"},
		"ghcr.io/org/app@sha256:abc":     {"ghcr.io", "org/app", "sha256:abc"},
		"harbor.local:8443/proj/app:v2":  {"harbor.local:8443", "proj/app", "v2"},
		"localhost/app":                  {"localhost", "app", "latest"},
		"ghcr.io/org/app:v1@sha256:abcd": {"ghcr.io", "org/app", "sha256:abcd"},
	}
	for input, want := range cases {
		got, err := ParseReference(input)
		if err != nil || got != want {
			t.Errorf("ParseReference(%q) = %+v, %v; want %+v", input, got, err, want)
		}
	}
}

func TestDownloadStatic(t *testing.T) {
	reg := fakeregistry.Start(fakeregistry.Options{})
	defer reg.Close()
	client, err := core.New(core.Config{BaseURL: reg.URL})
	if err != nil {
		t.Fatal(err)
	}
	spec := &orbv1.RandomImageSet{Layers: 2, LayerSize: orbv1.SizeDistribution{Distribution: "Fixed", Size: quantity("8Ki")}}
	pushed, err := client.PushImage(context.Background(), core.StartTiming(), "src/app", "v1", NewRandom(spec, 1).Image(1), 4096)
	if err != nil {
		t.Fatal(err)
	}

	host := mustURL(t, reg.URL).Host
	clientFor := func(string) (*core.Client, error) { return client, nil }
	static := &orbv1.StaticImageSet{Images: []string{host + "/src/app:v1"}, Platform: "linux/amd64", StorageLimit: quantity("1Mi")}

	set, err := DownloadStatic(context.Background(), static, t.TempDir(), clientFor)
	if err != nil {
		t.Fatal(err)
	}
	img := set.Images[0]
	if img.Digest != pushed.ManifestDigest {
		t.Errorf("resolved digest %s, want %s", img.Digest, pushed.ManifestDigest)
	}
	if got := readAll(t, img.Layers()[0]); int64(len(got)) != 8*1024 {
		t.Errorf("stored layer has %d bytes", len(got))
	}
	if resolved := set.Resolved(); resolved[0].Size != pushed.Bytes {
		t.Errorf("resolved size %d, want %d", resolved[0].Size, pushed.Bytes)
	}

	// Pushing the static image again must produce the same manifest.
	again, err := client.PushImage(context.Background(), core.StartTiming(), "dst/app", "v1", img, 4096)
	if err != nil || again.ManifestDigest != pushed.ManifestDigest {
		t.Fatalf("re-push gave %v, %v", again, err)
	}

	static.StorageLimit = quantity("1Ki")
	if _, err := DownloadStatic(context.Background(), static, t.TempDir(), clientFor); err == nil {
		t.Error("the storage limit was not enforced (IMG-9)")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
