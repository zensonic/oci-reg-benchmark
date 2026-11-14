// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Command orbctl is the command-line tool for ORB (section 14 of
// requirements.md).
package main

import (
	"os"

	"github.com/zensonic/oci-reg-benchmark/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
