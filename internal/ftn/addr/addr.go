// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package addr parses and renders FidoNet Technology Network (FTN)
// addresses, MSGID/REPLY kludge values, and the FTS-0001 AttributeWord.
// All forms here are FTSC-canonical — see FTS-0001, FTS-0009, FSC-0046,
// FSC-0054, FSC-0089, FRL-1004.
package addr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Addr is a 4D FTN address with an optional FSC-0089 domain.
type Addr struct {
	Zone, Net, Node, Point int
	Domain                 string
}

// Parse parses a 3D, 4D, or domain-form FTN address.
//
//	zone:net/node[.point][@domain]
//
// Missing point defaults to 0; missing domain leaves Addr.Domain empty.
func Parse(s string) (Addr, error) {
	if s == "" {
		return Addr{}, errors.New("ftn/addr: empty")
	}
	var domain string
	if i := strings.IndexByte(s, '@'); i >= 0 {
		domain = s[i+1:]
		if domain == "" {
			return Addr{}, errors.New("ftn/addr: empty domain after @")
		}
		s = s[:i]
	}
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return Addr{}, fmt.Errorf("ftn/addr: missing zone in %q", s)
	}
	slash := strings.IndexByte(s, '/')
	if slash <= colon+1 {
		return Addr{}, fmt.Errorf("ftn/addr: missing net in %q", s)
	}
	zone, err := strconv.Atoi(s[:colon])
	if err != nil {
		return Addr{}, fmt.Errorf("ftn/addr: zone: %w", err)
	}
	net, err := strconv.Atoi(s[colon+1 : slash])
	if err != nil {
		return Addr{}, fmt.Errorf("ftn/addr: net: %w", err)
	}
	rest := s[slash+1:]
	if rest == "" {
		return Addr{}, fmt.Errorf("ftn/addr: missing node in %q", s)
	}
	var nodeStr, pointStr string
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		nodeStr, pointStr = rest[:dot], rest[dot+1:]
	} else {
		nodeStr = rest
	}
	node, err := strconv.Atoi(nodeStr)
	if err != nil {
		return Addr{}, fmt.Errorf("ftn/addr: node: %w", err)
	}
	point := 0
	if pointStr != "" {
		point, err = strconv.Atoi(pointStr)
		if err != nil {
			return Addr{}, fmt.Errorf("ftn/addr: point: %w", err)
		}
	}
	return Addr{Zone: zone, Net: net, Node: node, Point: point, Domain: domain}, nil
}

// String renders the address in 4D form (always including .point),
// suffixed with @domain when Domain is non-empty.
func (a Addr) String() string {
	base := fmt.Sprintf("%d:%d/%d.%d", a.Zone, a.Net, a.Node, a.Point)
	if a.Domain != "" {
		return base + "@" + a.Domain
	}
	return base
}

// MSGID is a parsed FTS-0009 MSGID kludge value.
//
//	<origaddr> <serialno>
//
// origaddr may be a bare address or a double-quoted string with "" as
// embedded literal-quote escape. serialno is exactly eight hex digits.
type MSGID struct {
	Origin    Addr   // parsed origaddr; zero if OriginRaw is not a valid Addr
	OriginRaw string // verbatim origaddr text (unquoted on parse)
	Serial    uint32
}

// ParseMSGID parses the body of an FTS-0009 MSGID line (everything after
// "MSGID: "). It accepts both bare and quoted-origaddr forms.
func ParseMSGID(s string) (MSGID, error) {
	if s == "" {
		return MSGID{}, errors.New("ftn/addr: empty MSGID")
	}
	var raw, rest string
	if s[0] == '"' {
		// Quoted: walk until closing unescaped quote.
		var b strings.Builder
		i := 1
		for i < len(s) {
			c := s[i]
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i += 2
					continue
				}
				// End of quoted region.
				raw = b.String()
				rest = s[i+1:]
				goto done
			}
			b.WriteByte(c)
			i++
		}
		return MSGID{}, errors.New("ftn/addr: unterminated MSGID quote")
	} else {
		sp := strings.LastIndexByte(s, ' ')
		if sp < 0 {
			return MSGID{}, errors.New("ftn/addr: missing serial in MSGID")
		}
		raw = s[:sp]
		rest = s[sp:]
	}
done:
	rest = strings.TrimLeft(rest, " ")
	if rest == "" {
		return MSGID{}, errors.New("ftn/addr: missing serial in MSGID")
	}
	if strings.IndexByte(rest, ' ') >= 0 {
		return MSGID{}, fmt.Errorf("ftn/addr: trailing junk after serial in MSGID: %q", rest)
	}
	if len(rest) > 8 {
		return MSGID{}, fmt.Errorf("ftn/addr: serial too long: %q", rest)
	}
	n, err := strconv.ParseUint(rest, 16, 32)
	if err != nil {
		return MSGID{}, fmt.Errorf("ftn/addr: serial: %w", err)
	}
	m := MSGID{OriginRaw: raw, Serial: uint32(n)}
	if a, err := Parse(raw); err == nil {
		m.Origin = a
	}
	return m, nil
}

// String renders the MSGID as FTS-0009 wants it: the origaddr followed
// by a space and an 8-hex-lowercase serial. When OriginRaw is set it is
// used verbatim (and quoted if it would otherwise be ambiguous);
// otherwise Origin is formatted.
func (m MSGID) String() string {
	raw := m.OriginRaw
	if raw == "" {
		raw = m.Origin.String()
	}
	rendered := raw
	if needsQuoting(raw) {
		rendered = `"` + strings.ReplaceAll(raw, `"`, `""`) + `"`
	}
	return fmt.Sprintf("%s %08x", rendered, m.Serial)
}

func needsQuoting(s string) bool {
	return strings.ContainsAny(s, ` "`)
}

// ChrsValue is the FSC-0054 CHRS kludge value: name and level.
//
//	UTF-8 4
//	CP437 2
type ChrsValue struct {
	Name  string
	Level int
}

// ParseChrs parses a CHRS kludge value (everything after "CHRS: ").
func ParseChrs(s string) (ChrsValue, error) {
	sp := strings.LastIndexByte(s, ' ')
	if sp <= 0 || sp == len(s)-1 {
		return ChrsValue{}, fmt.Errorf("ftn/addr: bad CHRS: %q", s)
	}
	lvl, err := strconv.Atoi(s[sp+1:])
	if err != nil {
		return ChrsValue{}, fmt.Errorf("ftn/addr: CHRS level: %w", err)
	}
	return ChrsValue{Name: s[:sp], Level: lvl}, nil
}

// String renders the CHRS kludge value.
func (c ChrsValue) String() string {
	return fmt.Sprintf("%s %d", c.Name, c.Level)
}

// ParseTZUTC parses an FRL-1004 TZUTC value (`±HHMM`) and returns
// signed minutes east of UTC.
func ParseTZUTC(s string) (int, error) {
	if len(s) != 5 || (s[0] != '+' && s[0] != '-') {
		return 0, fmt.Errorf("ftn/addr: bad TZUTC: %q", s)
	}
	h, err := strconv.Atoi(s[1:3])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("ftn/addr: TZUTC hours: %q", s)
	}
	m, err := strconv.Atoi(s[3:5])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("ftn/addr: TZUTC minutes: %q", s)
	}
	val := h*60 + m
	if s[0] == '-' {
		val = -val
	}
	return val, nil
}

// FormatTZUTC renders signed minutes east of UTC as an FRL-1004 TZUTC value.
func FormatTZUTC(minutes int) string {
	sign := byte('+')
	if minutes < 0 {
		sign = '-'
		minutes = -minutes
	}
	return fmt.Sprintf("%c%02d%02d", sign, minutes/60, minutes%60)
}

// FTS-0001 AttributeWord bits.
const (
	AttrPrivate              uint16 = 1 << 0
	AttrCrash                uint16 = 1 << 1
	AttrRecd                 uint16 = 1 << 2
	AttrSent                 uint16 = 1 << 3
	AttrFileAttached         uint16 = 1 << 4
	AttrInTransit            uint16 = 1 << 5
	AttrOrphan               uint16 = 1 << 6
	AttrKillSent             uint16 = 1 << 7
	AttrLocal                uint16 = 1 << 8
	AttrHoldForPickup        uint16 = 1 << 9
	AttrUnused10             uint16 = 1 << 10
	AttrFileRequest          uint16 = 1 << 11
	AttrReturnReceiptRequest uint16 = 1 << 12
	AttrIsReturnReceipt      uint16 = 1 << 13
	AttrAuditRequest         uint16 = 1 << 14
	AttrFileUpdateReq        uint16 = 1 << 15
)
