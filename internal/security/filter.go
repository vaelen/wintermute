// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
)

// Emitter writes a flagged IPv4 dotted-quad string as a single UDP
// datagram to a subtext-filter bridge. Construction can fail if the
// configured address does not parse; once constructed, Emit is
// fire-and-forget: a failed send is logged at Warn but never blocks
// the caller. IPv6 inputs are skipped with a Warn log line.
type Emitter struct {
	addr   *net.UDPAddr
	logger *slog.Logger

	mu   sync.Mutex
	conn *net.UDPConn
}

// NewEmitter builds an Emitter for the given UDP address (e.g.
// "127.0.0.1:1234"). The address is resolved as udp4 so the caller
// gets a clear failure if it points at an IPv6 host. logger may be
// nil (a discard logger is used).
func NewEmitter(addr string, logger *slog.Logger) (*Emitter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("security: filter address %q: %w", addr, err)
	}
	conn, err := net.DialUDP("udp4", nil, udpAddr)
	if err != nil {
		return nil, fmt.Errorf("security: filter dial %q: %w", addr, err)
	}
	return &Emitter{
		addr:   udpAddr,
		logger: logger,
		conn:   conn,
	}, nil
}

// Emit sends a single UDP packet whose payload is the IPv4 dotted-quad
// string for addr, with no trailing newline. IPv6 sources are skipped
// (subtext-filter is IPv4-only) and logged at Warn. Send failures are
// logged at Warn and discarded — the emitter must never block the
// accept loop.
func (e *Emitter) Emit(addr netip.Addr) {
	if e == nil {
		return
	}
	if !addr.Is4() && !addr.Is4In6() {
		e.logger.Warn("subtext-filter: skip non-ipv4 flag", "ip", addr.String())
		return
	}
	v4 := addr.Unmap()
	e.mu.Lock()
	conn := e.conn
	e.mu.Unlock()
	if conn == nil {
		return
	}
	if _, err := conn.Write([]byte(v4.String())); err != nil {
		e.logger.Warn("subtext-filter: emit failed", "ip", v4.String(), "err", err)
	}
}

// Close releases the underlying connection. Safe to call multiple times.
func (e *Emitter) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn == nil {
		return nil
	}
	err := e.conn.Close()
	e.conn = nil
	return err
}
