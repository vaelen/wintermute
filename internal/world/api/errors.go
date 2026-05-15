// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import "fmt"

// Stable error codes returned by API operations. Lua bindings surface
// these in the canonical message format "wintermute: <code>: <details>".
const (
	CodeDuplicateSlug    = "duplicate_slug"
	CodeNotFound         = "not_found"
	CodeInvalidArgument  = "invalid_argument"
	CodePermissionDenied = "permission_denied"
	CodeInternal         = "internal"
)

// Error is the API's stable error type. Code is one of the Code* constants
// above; Details carries the offending value (typically a slug). The
// Error() method renders the canonical wintermute message format.
type Error struct {
	Code    string
	Details string
}

// Error renders the canonical "wintermute: <code>: <details>" format.
func (e *Error) Error() string {
	if e.Details == "" {
		return "wintermute: " + e.Code
	}
	return "wintermute: " + e.Code + ": " + e.Details
}

// errorf is a convenience constructor for typed errors with formatted
// details. The format and arguments follow fmt.Sprintf.
func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Details: fmt.Sprintf(format, args...)}
}

// duplicateSlug returns the canonical duplicate-slug error for a given slug.
func duplicateSlug(slug string) *Error {
	return &Error{Code: CodeDuplicateSlug, Details: slug}
}

// notFound returns the canonical not-found error for a (kind, slug) pair.
func notFound(kind, slug string) *Error {
	return &Error{Code: CodeNotFound, Details: kind + ":" + slug}
}
