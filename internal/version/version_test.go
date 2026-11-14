// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package version

import "testing"

func TestStringIncludesAllFields(t *testing.T) {
	Version, Commit, Date = "1.2.3", "abc123", "2026-11-14T00:00:00Z"

	got := String()
	want := "1.2.3 (commit abc123, built 2026-11-14T00:00:00Z)"

	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
