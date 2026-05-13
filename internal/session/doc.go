// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package session ties the connection acceptor, telnet/encoding stack, and
// auth layer together into a per-connection lifecycle: capability
// detection → confirmation prompt → login → command loop. The placeholder
// command loop runs the post-login `terminal` command and a "void room"
// stub; real world commands land in milestone 2.
package session
