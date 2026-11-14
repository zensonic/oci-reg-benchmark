// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Command orb is the single binary for the ORB controller and agent (ENG-5).
//
// The controller and agent subcommands are placeholders until the build
// phase starts. They exist so the build and release pipeline has something
// real to build, test and ship.
package main

import (
	"fmt"
	"os"

	"github.com/zensonic/oci-reg-benchmark/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("orb", version.String())
	case "controller", "agent":
		fmt.Fprintf(os.Stderr, "orb %s: not implemented yet\n", os.Args[1])
		os.Exit(1)
	default:
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: orb <controller|agent|version>")
}
