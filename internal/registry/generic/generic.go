// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package generic is the default registry module (REG-5). It uses only the
// standard OCI Distribution API through the core. Other registry modules
// embed it and override what their registry does differently.
package generic

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/module"
)

// Module is the generic registry module.
type Module struct{}

var _ module.Module = Module{}

// Name returns "generic".
func (Module) Name() string { return "generic" }

// Authenticator returns nil: the core's challenge-based authenticator is used.
func (Module) Authenticator() func(*core.Credentials, *http.Client) core.Authenticator {
	return nil
}

// Supports returns true for every operation.
func (Module) Supports(string) bool { return true }

// ChunkSizeLimits returns no limits.
func (Module) ChunkSizeLimits() (minSize, maxSize int64) { return 0, 0 }

// DetectVersion returns "": the OCI API has no version endpoint.
func (Module) DetectVersion(context.Context, module.Env) (string, error) { return "", nil }

// PrepareRepositories does nothing: OCI registries create repositories on
// the first push.
func (Module) PrepareRepositories(context.Context, module.Env, []string) error { return nil }

// DeleteRepositories deletes every manifest in the repositories with the
// standard manifest DELETE. Registries that do not allow deletion are
// reported, not treated as a failure (CLEAN-4).
func (Module) DeleteRepositories(ctx context.Context, env module.Env, repositories []string) (string, error) {
	deleted := 0
	for _, repo := range repositories {
		tags, err := env.Client.ListTags(ctx, core.StartTiming(), repo, 1000, true)
		if core.IsStatus(err, http.StatusNotFound) {
			continue // nothing was pushed to this repository
		}
		if err != nil {
			return "", fmt.Errorf("listing tags of %s: %w", repo, err)
		}

		digests := map[string]bool{}
		for _, tag := range tags.Names {
			head, err := env.Client.HeadManifest(ctx, core.StartTiming(), repo, tag)
			if err != nil {
				return "", fmt.Errorf("resolving %s:%s: %w", repo, tag, err)
			}
			if head.Exists && head.Digest != "" {
				digests[head.Digest] = true
			}
		}

		for digest := range digests {
			err := env.Client.DeleteManifest(ctx, repo, digest)
			if errors.Is(err, core.ErrUnsupported) {
				return "cleanup not possible: the registry does not allow deleting manifests", nil
			}
			if err != nil {
				return "", fmt.Errorf("deleting %s@%s: %w", repo, digest, err)
			}
			deleted++
		}
	}
	return fmt.Sprintf("deleted %d manifests in %d repositories", deleted, len(repositories)), nil
}
