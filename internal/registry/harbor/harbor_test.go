// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package harbor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/module"
)

// fakeHarbor answers the Harbor API calls the module makes.
func fakeHarbor(t *testing.T, deleted *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2.0/systeminfo":
			_, _ = w.Write([]byte(`{"harbor_version":"v2.13.0-abc"}`))
		case r.URL.Path == "/api/v2.0/projects/bench":
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/api/v2.0/projects/"):
			if r.Method == http.MethodDelete {
				*deleted = append(*deleted, r.URL.EscapedPath())
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
}

func TestHarborModule(t *testing.T) {
	var deleted []string
	server := fakeHarbor(t, &deleted)
	defer server.Close()

	client, err := core.New(core.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	env := module.Env{Client: client, Credentials: &core.Credentials{Username: "admin", Password: "x"}}
	ctx := context.Background()
	m := Module{}

	version, err := m.DetectVersion(ctx, env)
	if err != nil || version != "v2.13.0-abc" {
		t.Fatalf("version %q, %v", version, err)
	}

	if err := m.PrepareRepositories(ctx, env, []string{"bench/run-1/load/pool"}); err != nil {
		t.Errorf("existing project rejected: %v", err)
	}
	if err := m.PrepareRepositories(ctx, env, []string{"missing/run-1/x"}); err == nil {
		t.Error("missing project was not reported")
	}

	msg, err := m.DeleteRepositories(ctx, env, []string{"bench/run-1/load/pool"})
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "/api/v2.0/projects/bench/repositories/run-1%252Fload%252Fpool" {
		t.Errorf("deleted %v; repository names must be double-encoded", deleted)
	}
	if !strings.Contains(msg, "1 Harbor repositories") {
		t.Errorf("unexpected message %q", msg)
	}
}
