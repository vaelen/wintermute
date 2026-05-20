// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package security implements M6.6's login-hardening defences: a
// disallowed-username list (seeded from a SecLists shortlist), an
// in-process IP deny list with temporary + permanent rows, a
// failed-password sliding-window counter, the username-history
// reservation table, and an optional UDP emitter that flags IPs to
// the subtext-filter bridge.
//
// The package's Service is the sole writer of both the cache and the
// DB rows. Every mutation takes the service's own mutex, calls
// store.DB.Write inside the locked region, then updates the cache
// after the write commits — admin commands, auto-flag triggers, and
// the Lua surface all funnel through these methods so the cache and
// DB cannot drift.
package security
