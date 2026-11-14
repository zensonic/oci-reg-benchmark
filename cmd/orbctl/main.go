// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Command orbctl is the command-line tool for ORB (section 14 of
// requirements.md).
//
// Only the version command exists so far. The other commands will be added
// in the build phase.
package main

import (
	"fmt"
	"os"

	"github.com/zensonic/oci-reg-benchmark/internal/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println("orbctl", version.String())
		return
	}

	fmt.Fprintln(os.Stderr, "usage: orbctl version")
	fmt.Fprintln(os.Stderr, "Other commands are not implemented yet.")
	os.Exit(2)
}
