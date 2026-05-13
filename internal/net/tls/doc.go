// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package tls wraps crypto/tls for the engine's listener needs, including a
// self-signed cert path for development and an autocert path for production.
// See docs/milestones/01-connection-layer.md.
package tls
