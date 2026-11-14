// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package harbor holds the Harbor-specific handling (REG-3). Harbor speaks
// the standard OCI API for images, so most behaviour comes from the generic
// module. Harbor differs in three ways:
//
//   - Repositories live in projects, and a project must exist before a push.
//   - Whole repositories are deleted through Harbor's own REST API.
//   - The version is available from Harbor's system info API.
package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/zensonic/oci-reg-benchmark/internal/registry/generic"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/module"
)

// Module is the Harbor registry module.
type Module struct {
	generic.Module
}

var _ module.Module = Module{}

// Name returns "harbor".
func (Module) Name() string { return "harbor" }

// DetectVersion reads the version from /api/v2.0/systeminfo.
func (Module) DetectVersion(ctx context.Context, env module.Env) (string, error) {
	resp, err := api(ctx, env, http.MethodGet, "/api/v2.0/systeminfo")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reading Harbor system info: status %d", resp.StatusCode)
	}

	var info struct {
		HarborVersion string `json:"harbor_version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return "", fmt.Errorf("decoding Harbor system info: %w", err)
	}
	return info.HarborVersion, nil
}

// PrepareRepositories checks that the Harbor project of every repository
// exists. Harbor does not create projects on push.
func (Module) PrepareRepositories(ctx context.Context, env module.Env, repositories []string) error {
	for _, project := range projectsOf(repositories) {
		resp, err := api(ctx, env, http.MethodGet, "/api/v2.0/projects/"+url.PathEscape(project))
		if err != nil {
			return err
		}
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			continue
		case http.StatusNotFound:
			return fmt.Errorf("the Harbor project %q does not exist; create it first, or change registry.repositoryPrefix", project)
		default:
			// Some accounts, such as robot accounts, may not read project
			// details. The first push will show whether the project works.
			continue
		}
	}
	return nil
}

// DeleteRepositories deletes whole repositories through the Harbor API,
// which removes all their artifacts in one call per repository (CLEAN-4).
func (Module) DeleteRepositories(ctx context.Context, env module.Env, repositories []string) (string, error) {
	deleted := 0
	for _, repo := range repositories {
		project, name, found := strings.Cut(repo, "/")
		if !found {
			continue
		}
		// Harbor expects slashes in repository names to be encoded twice.
		path := fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s",
			url.PathEscape(project), url.PathEscape(url.PathEscape(name)))

		resp, err := api(ctx, env, http.MethodDelete, path)
		if err != nil {
			return "", err
		}
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			deleted++
		case http.StatusNotFound:
			// Nothing was pushed to this repository.
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Sprintf("cleanup not possible: the account may not delete repositories in project %q", project), nil
		default:
			return "", fmt.Errorf("deleting Harbor repository %s: status %d", repo, resp.StatusCode)
		}
	}
	return fmt.Sprintf("deleted %d Harbor repositories", deleted), nil
}

// api calls Harbor's REST API with basic auth over the client's connections.
func api(ctx context.Context, env module.Env, method, path string) (*http.Response, error) {
	// path is already escaped by the caller (repository names are encoded
	// twice), so it is appended as-is rather than escaped again.
	target := strings.TrimSuffix(env.Client.BaseURL().String(), "/") + path

	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if creds := env.Credentials; !creds.IsEmpty() && creds.Username != "" {
		req.SetBasicAuth(creds.Username, creds.Password)
	}
	resp, err := env.Client.HTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Harbor API %s: %w", path, err)
	}
	return resp, nil
}

// projectsOf returns the distinct projects (first path parts) of repositories.
func projectsOf(repositories []string) []string {
	seen := map[string]bool{}
	var projects []string
	for _, repo := range repositories {
		project, _, _ := strings.Cut(repo, "/")
		if !seen[project] {
			seen[project] = true
			projects = append(projects, project)
		}
	}
	return projects
}
