// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package integration hosts scenario tests that spin up the server on
// ephemeral ports and drive real telnet / TLS / HTTP sessions. Tests that
// depend on Ollama are guarded by the "ollama" build tag.
package integration
