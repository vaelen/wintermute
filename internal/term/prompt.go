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
	{'U', EncodingUTF8, "Unicode", "modern"},
	{'D', EncodingCP437, "DOS", "cp437"},
	{'M', EncodingMacRoman, "Mac", "classic"},
	{'L', EncodingISO88591, "Latin-1", ""},
	{'P', EncodingPETSCII, "PETSCII", ""},
	{'A', EncodingASCII, "ASCII", ""},
}

// RenderPrompt returns the confirmation prompt string with the [default]
// tag attached to the option whose Encoding matches defaultEnc. The bytes
// it returns are pure 7-bit ASCII so they render acceptably across all
// six encodings after the connect-time Shift Out has been sent.
//
// The first call to RenderPrompt also emits the welcome banner. Callers
// that want the prompt without the banner can use RenderPromptOnly.
func RenderPrompt(defaultEnc Encoding) string {
	return "Welcome to Wintermute.\r\n\r\n" + RenderPromptOnly(defaultEnc)
}

// RenderPromptOnly returns the prompt without the leading welcome banner.
func RenderPromptOnly(defaultEnc Encoding) string {
	var sb strings.Builder
	sb.WriteString("Terminal Type: ")
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
			tags = append(tags, "default")
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
func ParsePromptResponse(line string, defaultEnc Encoding) (enc Encoding, ok bool) {
	line = strings.TrimSpace(line)
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
