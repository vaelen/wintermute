// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import "fmt"

// Source distinguishes errors that originated on the Go side (a binding's
// argument type-check, for example) from those raised by Lua code itself
// (runtime errors, calls to error()).
type Source int

// Source values.
const (
	// SourceGo is for errors raised by Go bindings — e.g. a wrong
	// argument type passed to wintermute.room.create.
	SourceGo Source = iota
	// SourceLua is for errors raised inside the Lua VM — a script that
	// called error(), or a runtime panic translated by gopher-lua.
	SourceLua
)

// Error is the script package's stable error type. It carries the source
// of the error and the canonical "wintermute: <code>: <details>" message
// format used by every binding.
type Error struct {
	Source  Source
	Code    string
	Details string
}

// Error renders the canonical message format.
func (e *Error) Error() string {
	if e.Details == "" {
		return "wintermute: " + e.Code
	}
	return "wintermute: " + e.Code + ": " + e.Details
}

// goErrorf returns an Error from a Go binding with a formatted detail.
func goErrorf(code, format string, args ...any) *Error {
	return &Error{
		Source:  SourceGo,
		Code:    code,
		Details: fmt.Sprintf(format, args...),
	}
}

// luaError wraps a raw gopher-lua runtime error into our Error type.
// raw is the Go error returned by lua.LState.PCall, lua.LState.DoString,
// or similar.
func luaError(raw error) *Error {
	return &Error{
		Source:  SourceLua,
		Code:    "runtime",
		Details: raw.Error(),
	}
}
