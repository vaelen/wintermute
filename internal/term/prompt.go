// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import (
	"fmt"
	"strings"
)

// promptOption is one row of the connect-time confirmation prompt.
type promptOption struct {
	Letter   byte // single-letter shortcut, uppercase ASCII
	Encoding Encoding
	Name     string // visible name: "Unicode", "DOS", etc.
	Note     string // bracket-tag note (e.g. "modern", "cp437"); empty for plain names
}

var promptOptions = []promptOption{
	{'U', EncodingUTF8, "UNICODE", "MODERN"},
	{'D', EncodingCP437, "DOS", "CP437"},
	{'M', EncodingMacRoman, "MAC", "CLASSIC"},
	{'L', EncodingISO88591, "LATIN-1", ""},
	{'P', EncodingPETSCII, "PETSCII", ""},
	{'A', EncodingASCII, "ASCII", ""},
}

// RenderPrompt returns the confirmation prompt string with the [DEFAULT]
// tag attached to the option whose Encoding matches defaultEnc.
//
// The prompt is intentionally all uppercase, using only characters whose
// byte positions render the same in every supported encoding *and* on a
// PETSCII client in its default (uppercase / graphics) mode. That is what
// lets the prompt be readable to a fresh C64 connection without the
// engine first emitting a Shift Out — Shift Out is only sent when the
// session actually transitions into PETSCII.
func RenderPrompt(defaultEnc Encoding) string {
	return "WELCOME TO WINTERMUTE.\r\n\r\n" + RenderPromptOnly(defaultEnc)
}

// RenderPromptOnly returns the prompt without the leading welcome banner.
func RenderPromptOnly(defaultEnc Encoding) string {
	var sb strings.Builder
	sb.WriteString("TERMINAL TYPE: ")
	for i, opt := range promptOptions {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%c - %s", opt.Letter, opt.Name)

		var tags []string
		if opt.Note != "" {
			tags = append(tags, opt.Note)
		}
		if opt.Encoding == defaultEnc {
			tags = append(tags, "DEFAULT")
		}
		if len(tags) > 0 {
			sb.WriteString(" [")
			sb.WriteString(strings.Join(tags, ", "))
			sb.WriteByte(']')
		}
	}
	sb.WriteString(": ")
	return sb.String()
}

// ParsePromptResponse interprets a user response to the confirmation
// prompt. Empty input accepts the default. A single-letter response
// (case-insensitive) chooses that encoding. Anything else returns
// the default and ok=false so the caller can re-prompt.
//
// The session input layer strips ANSI CSI sequences from incoming
// lines already, but ParsePromptResponse is defensive: it strips CSI
// itself so callers that bypass the session layer (tests, future
// uses) still get correct behavior on inputs prefixed with a stray
// terminal auto-response like \x1B[?1;2c.
func ParsePromptResponse(line string, defaultEnc Encoding) (enc Encoding, ok bool) {
	line = strings.TrimSpace(StripCSI(line))
	if line == "" {
		return defaultEnc, true
	}
	letter := strings.ToUpper(line)[0]
	for _, opt := range promptOptions {
		if opt.Letter == letter {
			return opt.Encoding, true
		}
	}
	return defaultEnc, false
}
