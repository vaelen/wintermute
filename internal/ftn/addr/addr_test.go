// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package addr

import (
	"strings"
	"testing"
)

func TestParse_4DPlusDomain(t *testing.T) {
	a, err := Parse("1:234/5.6@fidonet")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Zone != 1 || a.Net != 234 || a.Node != 5 || a.Point != 6 || a.Domain != "fidonet" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_3DNoDomain(t *testing.T) {
	a, err := Parse("21:1/100")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Zone != 21 || a.Net != 1 || a.Node != 100 || a.Point != 0 || a.Domain != "" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_DomainOnlyNoPoint(t *testing.T) {
	a, err := Parse("1:2/3@fsxnet")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Domain != "fsxnet" || a.Point != 0 {
		t.Errorf("got %+v", a)
	}
}

func TestParse_Rejects_Malformed(t *testing.T) {
	cases := []string{
		"", "garbage", "1:2", "1:2/", "1:2/3.x", "x:2/3", "1:/3",
		"1:2/3.4@", "@domain", "1.2.3.4",
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("Parse(%q) should have failed", c)
		}
	}
}

func TestString_RoundTrip(t *testing.T) {
	for _, in := range []string{
		"1:234/5.6@fidonet",
		"21:1/100.0@fsxnet",
		"255:255/255.0@local",
	} {
		a, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got := a.String(); got != in {
			t.Errorf("round-trip: in=%q got=%q", in, got)
		}
	}
}

func TestString_NoDomainEmits4D(t *testing.T) {
	a := Addr{Zone: 1, Net: 2, Node: 3, Point: 0}
	if got := a.String(); got != "1:2/3.0" {
		t.Errorf("got %q", got)
	}
}

func TestParseMSGID_Bare(t *testing.T) {
	m, err := ParseMSGID("1:234/5.6@fidonet deadbeef")
	if err != nil {
		t.Fatalf("ParseMSGID: %v", err)
	}
	if m.OriginRaw != "1:234/5.6@fidonet" {
		t.Errorf("origin raw = %q", m.OriginRaw)
	}
	if m.Origin.Zone != 1 || m.Origin.Domain != "fidonet" {
		t.Errorf("origin %+v", m.Origin)
	}
	if m.Serial != 0xdeadbeef {
		t.Errorf("serial = %x", m.Serial)
	}
}

func TestParseMSGID_Quoted(t *testing.T) {
	m, err := ParseMSGID(`"weird addr with spaces" 0000abcd`)
	if err != nil {
		t.Fatalf("ParseMSGID: %v", err)
	}
	if m.OriginRaw != "weird addr with spaces" {
		t.Errorf("origin raw = %q", m.OriginRaw)
	}
	if m.Serial != 0xabcd {
		t.Errorf("serial = %x", m.Serial)
	}
	// OriginRaw isn't a valid address; Origin should be zero.
	if m.Origin.Zone != 0 {
		t.Errorf("Origin should be zero for unparseable raw")
	}
}

func TestParseMSGID_QuotedWithEmbeddedQuote(t *testing.T) {
	// FTS-0009: an embedded literal " is represented as "".
	m, err := ParseMSGID(`"he said ""hi""" 01020304`)
	if err != nil {
		t.Fatalf("ParseMSGID: %v", err)
	}
	want := `he said "hi"`
	if m.OriginRaw != want {
		t.Errorf("origin raw = %q want %q", m.OriginRaw, want)
	}
	if m.Serial != 0x01020304 {
		t.Errorf("serial = %x", m.Serial)
	}
}

func TestParseMSGID_Rejects(t *testing.T) {
	cases := []string{
		"",
		"1:2/3.4@local",          // no serial
		"1:2/3.4@local notahex",  // bad serial
		`"unterminated quote 12`, // bad quoting
		"1:2/3.4 deadbeef extra", // trailing junk after serial
	}
	for _, c := range cases {
		if _, err := ParseMSGID(c); err == nil {
			t.Errorf("ParseMSGID(%q) should have failed", c)
		}
	}
}

func TestMSGIDString_Bare(t *testing.T) {
	m := MSGID{
		Origin: Addr{Zone: 1, Net: 2, Node: 3, Point: 0, Domain: "local"},
		Serial: 0x01,
	}
	want := "1:2/3.0@local 00000001"
	if got := m.String(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestMSGIDString_PreservesOriginRaw(t *testing.T) {
	m := MSGID{OriginRaw: "verbatim addr", Serial: 0xff}
	want := `"verbatim addr" 000000ff`
	if got := m.String(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestMSGIDString_QuotesEmbeddedQuotes(t *testing.T) {
	m := MSGID{OriginRaw: `he said "hi"`, Serial: 1}
	want := `"he said ""hi""" 00000001`
	if got := m.String(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestParseChrs(t *testing.T) {
	c, err := ParseChrs("UTF-8 4")
	if err != nil || c.Name != "UTF-8" || c.Level != 4 {
		t.Errorf("got %+v err %v", c, err)
	}
	c2, err := ParseChrs("CP437 2")
	if err != nil || c2.Name != "CP437" || c2.Level != 2 {
		t.Errorf("got %+v err %v", c2, err)
	}
	if _, err := ParseChrs(""); err == nil {
		t.Errorf("empty should fail")
	}
	if _, err := ParseChrs("X notanint"); err == nil {
		t.Errorf("non-int level should fail")
	}
}

func TestChrsString(t *testing.T) {
	c := ChrsValue{Name: "UTF-8", Level: 4}
	if got := c.String(); got != "UTF-8 4" {
		t.Errorf("got %q", got)
	}
}

func TestParseTZUTC(t *testing.T) {
	cases := map[string]int{
		"+0000": 0,
		"-0000": 0,
		"+0530": 5*60 + 30,
		"-0800": -8 * 60,
		"+1400": 14 * 60,
	}
	for in, want := range cases {
		got, err := ParseTZUTC(in)
		if err != nil {
			t.Errorf("ParseTZUTC(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseTZUTC(%q) = %d want %d", in, got, want)
		}
	}
}

func TestParseTZUTC_Rejects(t *testing.T) {
	for _, in := range []string{"", "0000", "+abcd", "+12345", "+99"} {
		if _, err := ParseTZUTC(in); err == nil {
			t.Errorf("ParseTZUTC(%q) should fail", in)
		}
	}
}

func TestFormatTZUTC(t *testing.T) {
	cases := map[int]string{
		0:           "+0000",
		5*60 + 30:   "+0530",
		-8 * 60:     "-0800",
		-(60 + 30):  "-0130",
		14 * 60:     "+1400",
	}
	for in, want := range cases {
		if got := FormatTZUTC(in); got != want {
			t.Errorf("FormatTZUTC(%d) = %q want %q", in, got, want)
		}
	}
}

func TestAttributeWord_KnownBits(t *testing.T) {
	if AttrPrivate != 1<<0 {
		t.Errorf("AttrPrivate")
	}
	if AttrCrash != 1<<1 {
		t.Errorf("AttrCrash")
	}
	if AttrFileAttached != 1<<4 {
		t.Errorf("AttrFileAttached")
	}
	if AttrKillSent != 1<<7 {
		t.Errorf("AttrKillSent")
	}
	if AttrFileUpdateReq != 1<<15 {
		t.Errorf("AttrFileUpdateReq")
	}
}

func TestSerialHex_Lowercase(t *testing.T) {
	// Per convention, serials render as lowercase 8-hex.
	m := MSGID{
		Origin: Addr{Zone: 1, Net: 2, Node: 3, Point: 0, Domain: "x"},
		Serial: 0xDEADBEEF,
	}
	if !strings.HasSuffix(m.String(), " deadbeef") {
		t.Errorf("expected lowercase hex suffix, got %q", m.String())
	}
}
