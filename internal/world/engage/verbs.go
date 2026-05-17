// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import "strings"

// MatchEngageVerb tries each host's EngageVerbs as a case-insensitive
// prefix of line. On the first match, returns the host, the residue of
// the line (target reference, preserved in its original case), and ok=true.
//
// Longer verbs are tried first within a single host's list so "sit at"
// beats "sit"; the host order is preserved across hosts (caller-controlled).
func MatchEngageVerb(line string, hosts []*Host) (host *Host, target string, ok bool) {
	lower := strings.ToLower(line)
	for _, h := range hosts {
		for _, v := range sortedByLengthDesc(h.EngageVerbs) {
			vLower := strings.ToLower(v)
			if strings.HasPrefix(lower, vLower) {
				rest := line[len(v):]
				if len(rest) > 0 && rest[0] != ' ' && rest[0] != '\t' {
					continue
				}
				target = strings.TrimSpace(rest)
				if target == "" {
					continue
				}
				return h, target, true
			}
		}
	}
	return nil, "", false
}

// MatchUniversalEngageVerb checks for the universal "engage <target>" /
// "engage with <target>" forms. Returns the trimmed target and ok=true
// on match.
func MatchUniversalEngageVerb(line string) (target string, ok bool) {
	lower := strings.ToLower(strings.TrimSpace(line))
	const a = "engage with "
	const b = "engage "
	switch {
	case strings.HasPrefix(lower, a):
		target = strings.TrimSpace(line[len(a):])
	case lower == "engage with":
		return "", false
	case strings.HasPrefix(lower, b):
		target = strings.TrimSpace(line[len(b):])
	default:
		return "", false
	}
	if target == "" {
		return "", false
	}
	return target, true
}

// MatchDisengageVerb reports whether line is a disengage command for the
// host: either the universal "disengage" or any of host.DisengageVerbs
// (exact, case-insensitive, after trimming).
func MatchDisengageVerb(line string, host *Host) bool {
	t := strings.ToLower(strings.TrimSpace(line))
	if t == "disengage" {
		return true
	}
	if host == nil {
		return false
	}
	for _, v := range host.DisengageVerbs {
		if t == strings.ToLower(v) {
			return true
		}
	}
	return false
}

// sortedByLengthDesc returns a new slice with the longer strings first.
// Stable for equal-length runs (insertion order preserved among equals).
func sortedByLengthDesc(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j]) > len(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
