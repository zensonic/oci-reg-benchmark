// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package module defines the interface that registry modules implement
// (REG-4). A registry module holds everything one specific registry needs
// that the common core does not do. Modules embed generic.Module and
// override only the hooks where their registry differs.
package module

import (
	"context"
	"net/http"

	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
)

// Env gives a module access to the registry.
type Env struct {
	// Client is a core client for the registry under test.
	Client *core.Client
	// Credentials are the registry credentials, or nil for anonymous access.
	Credentials *core.Credentials
}

// Module is the set of registry-specific hooks.
type Module interface {
	// Name is the module name, which is the registry's name (REG-3).
	Name() string

	// Authenticator returns a factory for a registry-specific authenticator,
	// or nil to use the core's challenge-based authenticator (section 13).
	Authenticator() func(creds *core.Credentials, httpClient *http.Client) core.Authenticator

	// Supports reports whether the registry supports an operation, using the
	// operation names from the API (REG-7).
	Supports(operation string) bool

	// ChunkSizeLimits returns the smallest and largest chunk size the
	// registry accepts for uploads. Zero means no limit.
	ChunkSizeLimits() (minSize, maxSize int64)

	// DetectVersion returns the registry version, or "" if it is unknown.
	DetectVersion(ctx context.Context, env Env) (string, error)

	// PrepareRepositories makes sure the run can push to the repositories,
	// for example by checking that a Harbor project exists.
	PrepareRepositories(ctx context.Context, env Env, repositories []string) error

	// DeleteRepositories removes everything the run pushed to the
	// repositories (CLEAN-4). It returns a short description of what it did.
	DeleteRepositories(ctx context.Context, env Env, repositories []string) (string, error)
}
