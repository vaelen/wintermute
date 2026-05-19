// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package term

import "strings"

// DetectHints carries information gathered during connection acceptance,
// before the user has been prompted to confirm capabilities. The fields
// are filled in by the network layer (telnet peek, IAC subnegotiation)
// and by scanning probe responses (ANSI Device Attributes, Secondary DA,
// XTVERSION, CSI 18 t window size, cursor position report).
//
// Sources of truth for each axis:
//   - TermType:   TTYPE (telnet) > XTVersion > SecondaryDAType decode.
//     First non-empty wins so a telnet client with TTYPE
//     keeps the user-facing identifier even when an ANSI
//     probe reply arrives.
//   - Width:      NAWSWidth > WinCols > CursorCols. First positive value.
//   - Height:     NAWSHeight > WinRows > CursorRows. First positive value.
//   - ANSI:       true if any DA/Secondary DA reply seen.
type DetectHints struct {
	Telnet      bool
	TermType    string
	NAWSWidth   int
	NAWSHeight  int
	ANSICapable bool

	// SecondaryDAType is the type code from a Secondary Device
	// Attributes reply (CSI > t;v;h c). Known codes:
	//
	//	  0  -> "vt100"
	//	  1  -> "vt220"
	//	 41  -> "xterm"
	//	 65  -> "vt500"
	//	 77  -> "mintty"
	//	 82  -> "rxvt"
	//	 84  -> "tmux"
	//	 85  -> "rxvt-unicode"
	//
	// Zero means "no Secondary DA reply observed". A real VT100 also
	// reports 0, but distinguishing "absent" from "VT100 saying 0" is
	// not worth the bookkeeping — both fall back to no TermType hint.
	SecondaryDAType int

	// XTVersion is the DCS-wrapped name+version string from an
	// XTVERSION reply (e.g. "xterm(384)", "WezTerm 20240814...",
	// "kitty 0.35.2", "tmux 3.4"). Empty when not reported.
	XTVersion string

	// WinRows / WinCols are the cell-grid dimensions reported by a
	// CSI 18 t reply (CSI 8 ; rows ; cols t). Zero when not reported.
	WinRows, WinCols int

	// CursorRows / CursorCols are the position reported by a CSI 6 n
	// (Cursor Position Report) reply after the engine clamped the
	// cursor to the bottom-right (CSI 999;999H). The bottom-right
	// position is the terminal's grid size. Zero when not reported.
	CursorRows, CursorCols int
}

// AutoDetect combines the hints into a Capabilities default. Color and
// DEC line drawing fall out of the encoding defaults via ApplyEncodingDefaults.
//
// Precedence is documented on DetectHints. Briefly: TTYPE > XTVERSION >
// Secondary DA for TermType; NAWS > CSI 18 t > CPR for Width/Height.
func AutoDetect(h DetectHints) Capabilities {
	termType := h.ResolveTermType()
	caps := Capabilities{
		Telnet:   h.Telnet,
		TermType: termType,
		ANSI:     h.ANSICapable,
	}

	if enc, ok := encodingFromTTYPE(termType); ok {
		caps.Encoding = enc
	} else if h.ANSICapable {
		caps.Encoding = EncodingUTF8
	} else {
		caps.Encoding = EncodingASCII
	}

	caps.Width = h.ResolveWidth()
	caps.Height = h.ResolveHeight()

	return caps.ApplyEncodingDefaults()
}

// ResolveTermType applies the precedence rules and returns the final
// TermType string for the hints. Returns "" when nothing is known.
func (h DetectHints) ResolveTermType() string {
	if h.TermType != "" {
		return h.TermType
	}
	if h.XTVersion != "" {
		return xtVersionToTermType(h.XTVersion)
	}
	if h.SecondaryDAType != 0 {
		return secondaryDATypeToTermType(h.SecondaryDAType)
	}
	return ""
}

// ResolveWidth applies the precedence rules and returns the final
// terminal width. Returns 0 when nothing is known so callers can fall
// back to encoding defaults.
func (h DetectHints) ResolveWidth() int {
	if h.NAWSWidth > 0 {
		return h.NAWSWidth
	}
	if h.WinCols > 0 {
		return h.WinCols
	}
	if h.CursorCols > 0 {
		return h.CursorCols
	}
	return 0
}

// ResolveHeight is the row-axis equivalent of ResolveWidth.
func (h DetectHints) ResolveHeight() int {
	if h.NAWSHeight > 0 {
		return h.NAWSHeight
	}
	if h.WinRows > 0 {
		return h.WinRows
	}
	if h.CursorRows > 0 {
		return h.CursorRows
	}
	return 0
}

// secondaryDATypeToTermType maps Secondary DA type codes to canonical
// TermType strings used by the engine. Unknown codes return "" so the
// caller can keep searching for a better source.
func secondaryDATypeToTermType(t int) string {
	switch t {
	case 0:
		return "vt100"
	case 1:
		return "vt220"
	case 24:
		return "vt240"
	case 41:
		return "xterm"
	case 61:
		return "vt510"
	case 64:
		return "vt420"
	case 65:
		return "vt500"
	case 77:
		return "mintty"
	case 82:
		return "rxvt"
	case 84:
		return "tmux"
	case 85:
		return "rxvt-unicode"
	}
	return ""
}

// xtVersionToTermType extracts a canonical family name from an XTVERSION
// reply. The reply is a free-form string (e.g. "xterm(384)", "WezTerm
// 20240814-114907-3b2eba32", "kitty 0.35.2", "tmux 3.4"); we lowercase
// and pick a known prefix word so the resulting TermType triggers the
// encoding heuristics in encodingFromTTYPE. Falls back to returning the
// whole string when nothing is recognised — losing nothing, since a
// non-empty TermType is always better than none.
func xtVersionToTermType(v string) string {
	low := strings.ToLower(strings.TrimSpace(v))
	if low == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(low, "xterm"):
		return "xterm"
	case strings.HasPrefix(low, "wezterm"):
		return "wezterm"
	case strings.HasPrefix(low, "kitty"):
		return "kitty"
	case strings.HasPrefix(low, "tmux"):
		return "tmux"
	case strings.HasPrefix(low, "alacritty"):
		return "alacritty"
	case strings.HasPrefix(low, "mintty"):
		return "mintty"
	case strings.HasPrefix(low, "rxvt-unicode"), strings.HasPrefix(low, "urxvt"):
		return "rxvt-unicode"
	case strings.HasPrefix(low, "rxvt"):
		return "rxvt"
	case strings.HasPrefix(low, "vte"):
		return "vte"
	case strings.HasPrefix(low, "iterm"):
		return "iterm"
	case strings.HasPrefix(low, "foot"):
		return "foot"
	}
	return v
}

// encodingFromTTYPE picks an encoding from a telnet TTYPE string. The
// match is loose substring on a lowercased copy. Returns false if no
// useful hint can be inferred.
func encodingFromTTYPE(ttype string) (Encoding, bool) {
	t := strings.ToLower(ttype)
	switch {
	case t == "":
		return 0, false
	case strings.Contains(t, "petscii"),
		strings.Contains(t, "c64"),
		strings.Contains(t, "c128"),
		strings.Contains(t, "cbm"):
		return EncodingPETSCII, true
	case strings.Contains(t, "cp437"),
		strings.Contains(t, "ibmpc"),
		strings.Contains(t, "ibm-pc"),
		strings.Contains(t, "syncterm"),
		strings.Contains(t, "ansi-bbs"):
		return EncodingCP437, true
	case strings.Contains(t, "ansi"),
		strings.Contains(t, "xterm"),
		strings.Contains(t, "vt100"),
		strings.Contains(t, "vt220"),
		strings.Contains(t, "screen"),
		strings.Contains(t, "tmux"),
		strings.Contains(t, "rxvt"):
		return EncodingUTF8, true
	}
	return 0, false
}

// ANSIProbe is the byte sequence the engine sends to elicit a primary
// Device Attributes response from an ANSI-capable terminal. The terminal
// auto-replies with ESC [ ? <digits and semicolons> c if it understands
// the query. Non-ANSI terminals send nothing.
//
// The engine sends ANSIProbe alongside the "press enter to begin"
// prompt: cooked-mode terminals line-buffer the response together with
// the user's Enter keystroke, so by the time the server reads the
// resulting line the response (if any) is right there at the start.
var ANSIProbe = []byte("\x1B[c")

// Probe byte sequences. Each is sent once during runDetect; terminals
// that don't understand a given probe simply stay silent. Save/move/CPR/
// restore is bundled with the other probes in AllProbes so the cursor
// excursion isn't visible to the user.
var (
	// SecondaryDAProbe asks for the Secondary Device Attributes reply
	// (CSI > c → CSI > <type>;<version>;<hardware> c).
	SecondaryDAProbe = []byte("\x1B[>c")
	// XTVERSIONProbe asks for the terminal name+version as a DCS reply
	// (CSI > 0 q → ESC P > | <text> ESC \ or ESC P > | <text> BEL).
	XTVERSIONProbe = []byte("\x1B[>0q")
	// WindowSizeProbe asks for the cell-grid dimensions as a CSI reply
	// (CSI 18 t → CSI 8 ; rows ; cols t). Supported by xterm-family
	// terminals; absent on bare VT100.
	WindowSizeProbe = []byte("\x1B[18t")
	// CursorReportProbe is the save/move/CPR/restore sequence. CSI s
	// saves the cursor; CSI 999;999H clamps to the bottom-right
	// (terminals don't move past their grid boundary, so the resulting
	// position IS the grid size); CSI 6 n asks for the position; CSI u
	// restores. Bundled as a single Write so the bytes travel together
	// and the user never sees a cursor jump.
	CursorReportProbe = []byte("\x1B[s\x1B[999;999H\x1B[6n\x1B[u")
)

// AllProbes returns the byte sequence of every detection probe in the
// order they should be sent. Callers Write the whole blob in one shot
// (so the cursor save/move/restore stays contiguous), then read the
// resulting replies and pass them to ScanProbeReplies.
func AllProbes() []byte {
	out := make([]byte, 0,
		len(ANSIProbe)+len(SecondaryDAProbe)+len(XTVERSIONProbe)+
			len(WindowSizeProbe)+len(CursorReportProbe))
	out = append(out, ANSIProbe...)
	out = append(out, SecondaryDAProbe...)
	out = append(out, XTVERSIONProbe...)
	out = append(out, WindowSizeProbe...)
	out = append(out, CursorReportProbe...)
	return out
}

// ScanProbeReplies parses buf for any terminal auto-responses and merges
// what it finds into h. Existing non-zero / non-empty fields are
// preserved — first observation wins, so calling ScanProbeReplies more
// than once is safe (e.g. login calls it on a press-enter read and then
// later on a follow-up read).
//
// Recognised shapes:
//   - Primary DA:    ESC [ ? <params> c                → sets ANSICapable
//   - Secondary DA:  ESC [ > <type>;<ver>;<hw> c        → sets SecondaryDAType
//     (also sets ANSICapable; a Secondary DA reply means
//     the terminal does CSI)
//   - XTVERSION DCS: ESC P > | <text> (ESC \ | BEL)     → sets XTVersion
//   - Window size:   ESC [ 8 ; <rows> ; <cols> t        → sets WinRows/Cols
//   - Cursor pos:    ESC [ <rows> ; <cols> R            → sets CursorRows/Cols
//
// Unrecognised escape sequences and ordinary text bytes are ignored.
func ScanProbeReplies(buf []byte, h *DetectHints) {
	for i := 0; i < len(buf); {
		b := buf[i]
		if b != 0x1B {
			i++
			continue
		}
		// CSI: ESC [
		if i+1 < len(buf) && buf[i+1] == '[' {
			end := findCSITerminator(buf, i)
			if end == -1 {
				return
			}
			scanCSIReply(buf[i:end+1], h)
			i = end + 1
			continue
		}
		// DCS: ESC P
		if i+1 < len(buf) && buf[i+1] == 'P' {
			end, ok := findDCSTerminator(buf, i)
			if !ok {
				return
			}
			scanDCSReply(buf[i:end], h)
			i = end
			continue
		}
		// Other ESC sequences — skip the introducer and let the loop
		// re-scan from the next byte. Designators (ESC ( x) etc. are
		// 3 bytes; advancing by 1 each iteration handles them safely.
		i++
	}
}

// scanCSIReply dispatches a single CSI sequence (full byte range,
// including the ESC[ prefix and the final byte) to the matching
// reply-shape parser. seq is guaranteed non-empty and well-formed.
func scanCSIReply(seq []byte, h *DetectHints) {
	if len(seq) < 3 {
		return
	}
	final := seq[len(seq)-1]
	// Body is everything between ESC[ and the final byte.
	body := seq[2 : len(seq)-1]
	switch final {
	case 'c':
		// 'c' is either primary DA (ESC[?...c) or Secondary DA
		// (ESC[>...c). Both indicate an ANSI-capable terminal.
		if len(body) == 0 {
			return
		}
		switch body[0] {
		case '?':
			h.ANSICapable = true
		case '>':
			h.ANSICapable = true
			if h.SecondaryDAType == 0 {
				parts := parseCSIParams(body[1:])
				if len(parts) >= 1 {
					h.SecondaryDAType = parts[0]
				}
			}
		}
	case 't':
		// Window-size reply: ESC[8;rows;cols t
		parts := parseCSIParams(body)
		if len(parts) >= 3 && parts[0] == 8 {
			if h.WinRows == 0 {
				h.WinRows = parts[1]
			}
			if h.WinCols == 0 {
				h.WinCols = parts[2]
			}
		}
	case 'R':
		// CPR reply: ESC[rows;cols R
		parts := parseCSIParams(body)
		if len(parts) >= 2 {
			if h.CursorRows == 0 {
				h.CursorRows = parts[0]
			}
			if h.CursorCols == 0 {
				h.CursorCols = parts[1]
			}
		}
	}
}

// scanDCSReply parses a DCS reply (ESC P ... ST). For XTVERSION the
// body starts with "> | " and the remainder is the terminal name/version
// string. seq includes the leading ESC P and the trailing ST (either
// ESC backslash or BEL).
func scanDCSReply(seq []byte, h *DetectHints) {
	// Strip the ESC P prefix and the ST terminator.
	if len(seq) < 3 || seq[0] != 0x1B || seq[1] != 'P' {
		return
	}
	body := seq[2:]
	// Trim the terminator: ESC \ (two bytes) or BEL (one byte).
	switch {
	case len(body) >= 2 && body[len(body)-2] == 0x1B && body[len(body)-1] == '\\':
		body = body[:len(body)-2]
	case len(body) >= 1 && body[len(body)-1] == 0x07:
		body = body[:len(body)-1]
	default:
		return
	}
	// XTVERSION reply prefix: "> | <name> [version]". The leading
	// "> | " is constant for XTVERSION; other DCS replies (Secondary
	// DA Request as a DCS sub-reply, request-status-string) are
	// ignored.
	if len(body) < 3 || body[0] != '>' || body[1] != '|' {
		return
	}
	rest := body[2:]
	rest = trimLeadingSpace(rest)
	if h.XTVersion == "" {
		h.XTVersion = string(rest)
	}
}

// findDCSTerminator returns the byte-index just past the end of a DCS
// sequence starting at start (which must point at ESC of an ESC P
// introducer). Returns (index, true) on a complete sequence — the
// index points to the byte AFTER the terminator. Returns (0, false) if
// the sequence is unterminated within buf.
//
// DCS terminator is either ST (ESC \ — two bytes) or BEL (0x07 — one
// byte). The body itself may contain printable ASCII plus a handful of
// control bytes; we scan conservatively and stop at the first
// terminator candidate.
func findDCSTerminator(buf []byte, start int) (int, bool) {
	if start+1 >= len(buf) || buf[start] != 0x1B || buf[start+1] != 'P' {
		return 0, false
	}
	for i := start + 2; i < len(buf); i++ {
		if buf[i] == 0x07 {
			return i + 1, true
		}
		if buf[i] == 0x1B && i+1 < len(buf) && buf[i+1] == '\\' {
			return i + 2, true
		}
	}
	return 0, false
}

// parseCSIParams splits a CSI parameter body on ';' and returns the
// numeric values. Empty params are treated as 0 so position-sensitive
// callers (CPR's rows/cols, the 8 in CSI 8;r;c t) can index reliably.
func parseCSIParams(body []byte) []int {
	if len(body) == 0 {
		return nil
	}
	var out []int
	var n int
	var hasDigit bool
	for _, b := range body {
		switch {
		case b >= '0' && b <= '9':
			n = n*10 + int(b-'0')
			hasDigit = true
		case b == ';':
			if hasDigit {
				out = append(out, n)
			} else {
				out = append(out, 0)
			}
			n = 0
			hasDigit = false
		default:
			// Ignore intermediate / private bytes.
		}
	}
	if hasDigit {
		out = append(out, n)
	} else if len(out) > 0 {
		out = append(out, 0)
	}
	return out
}

// trimLeadingSpace drops leading ASCII spaces and tabs from b.
func trimLeadingSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return b[i:]
}

// HasDAResponse reports whether buf contains a complete ANSI Device
// Attributes response.
func HasDAResponse(buf []byte) bool {
	return findDAResponseEnd(buf) != -1
}

// findDAResponseEnd returns the index of the 'c' that terminates an
// ANSI Device Attributes response (ESC [ ? <digits and semicolons> c)
// within buf, or -1 if no complete response is present. Bytes between
// '?' and 'c' must be digits or ';' for the response to be recognized.
func findDAResponseEnd(buf []byte) int {
	for i := 0; i+2 < len(buf); i++ {
		if buf[i] != 0x1B || buf[i+1] != '[' || buf[i+2] != '?' {
			continue
		}
		for j := i + 3; j < len(buf); j++ {
			b := buf[j]
			if b == 'c' {
				return j
			}
			if !((b >= '0' && b <= '9') || b == ';') {
				break
			}
		}
		// This ESC[? prefix didn't lead to a valid response; keep scanning
		// in case a real one sits further along in the buffer.
	}
	return -1
}
