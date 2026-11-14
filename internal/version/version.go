// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package version holds build information. The values are set at build time
// with -ldflags by the Makefile, the Dockerfile and GoReleaser.
package version

import "fmt"

// These defaults are used for local development builds.
var (
	// Version is the release version, for example "1.2.3".
	Version = "dev"

	// Commit is the git commit the binary was built from.
	Commit = "unknown"

	// Date is the build time in RFC 3339 format.
	Date = "unknown"
)

// String returns a one-line, human-readable description of the build.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
