// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestEmitterRoundTripIPv4(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer pc.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	em, err := NewEmitter(pc.LocalAddr().String(), logger)
	if err != nil {
		t.Fatalf("NewEmitter: %v", err)
	}
	defer em.Close()

	em.Emit(netip.MustParseAddr("192.0.2.42"))

	_ = pc.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	n, _, err := pc.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ReadFromUDP: %v", err)
	}
	if got, want := string(buf[:n]), "192.0.2.42"; got != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
	// No trailing newline.
	if bytes.HasSuffix(buf[:n], []byte("\n")) {
		t.Errorf("payload should not end in newline; got %q", buf[:n])
	}
}

func TestEmitterSkipsIPv6(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer pc.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	em, err := NewEmitter(pc.LocalAddr().String(), logger)
	if err != nil {
		t.Fatalf("NewEmitter: %v", err)
	}
	defer em.Close()
	em.Emit(netip.MustParseAddr("2001:db8::1"))
	_ = pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 16)
	if _, _, err := pc.ReadFromUDP(buf); err == nil {
		t.Errorf("expected no payload for IPv6, got: %q", buf)
	}
}

func TestEmitterInvalidAddress(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewEmitter("not-a-valid:addr:::", logger); err == nil {
		t.Errorf("expected error for malformed address")
	}
}
