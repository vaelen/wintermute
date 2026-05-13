// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Command wintermute is the Wintermute MUD/MUSH server.
//
// The full server is built incrementally across the milestones documented
// under docs/milestones/. Milestone 1 replaces this stub with the real entry
// point (config loading, listener startup, signal handling).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "wintermute: not yet implemented — see docs/milestones/01-connection-layer.md")
	os.Exit(1)
}
