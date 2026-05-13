// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package telnet

import (
	"bufio"
	"errors"
	"io"
	"net"
	"sync"
)

// Options controls the initial set of telnet options the server offers
// on a freshly wrapped connection.
type Options struct {
	OfferTTYPE   bool
	OfferNAWS    bool
	OfferCHARSET bool
	OfferSGA     bool
}

// DefaultOptions returns the set of options the engine offers by default.
func DefaultOptions() Options {
	return Options{
		OfferTTYPE:   true,
		OfferNAWS:    true,
		OfferCHARSET: true,
		OfferSGA:     true,
	}
}

// State holds the values that the IAC parser has learned about the
// remote terminal. Each field is updated as subnegotiations arrive.
type State struct {
	TermType string
	Width    int
	Height   int
	Charset  string
}

// Conn wraps a net.Conn that speaks the telnet protocol. The Read /
// Write methods present a clean byte stream to the engine; IAC sequences
// are consumed and replied to internally.
type Conn struct {
	raw net.Conn
	br  *bufio.Reader

	wmu sync.Mutex // serializes writes

	smu   sync.Mutex
	state State

	// Parser state. The parser is byte-at-a-time and lives on the Conn
	// rather than as a local in Read so it survives across Read calls.
	parseState parseState
	sbOption   byte
	sbBuf      []byte
}

// Wrap returns a Conn that uses raw for I/O and br for buffered reads.
// The initial set of option offers is sent immediately.
//
// br must be the same buffered reader passed to Detect; otherwise the
// bytes already buffered for option negotiation would be lost.
func Wrap(raw net.Conn, br *bufio.Reader, opts Options) (*Conn, error) {
	c := &Conn{raw: raw, br: br}
	return c, c.sendInitialOffers(opts)
}

// State returns a snapshot of the negotiated state.
func (c *Conn) State() State {
	c.smu.Lock()
	defer c.smu.Unlock()
	return c.state
}

// SetEcho controls server-side echo. When suppress is true we send
// IAC WILL ECHO, which signals the client to stop echoing locally —
// the typical password-entry posture. When false, IAC WONT ECHO is sent.
func (c *Conn) SetEcho(suppress bool) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if suppress {
		_, err := c.raw.Write([]byte{cmdIAC, cmdWILL, optEcho})
		return err
	}
	_, err := c.raw.Write([]byte{cmdIAC, cmdWONT, optEcho})
	return err
}

// Close closes the underlying connection.
func (c *Conn) Close() error {
	return c.raw.Close()
}

// LocalAddr / RemoteAddr expose the underlying connection's addresses.
func (c *Conn) LocalAddr() net.Addr  { return c.raw.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }

// ProcessBuffered drains any IAC sequences that are already buffered on
// the underlying reader. Returns immediately when the buffer is empty or
// when the next byte would be data (so subsequent Read calls still see
// it). Used during connection setup to give option negotiation a chance
// to settle before computing capability defaults.
func (c *Conn) ProcessBuffered() error {
	for c.br.Buffered() > 0 {
		peek, err := c.br.Peek(1)
		if err != nil || len(peek) == 0 {
			return err
		}
		if c.parseState == stNormal && peek[0] != cmdIAC {
			return nil
		}
		b, err := c.br.ReadByte()
		if err != nil {
			return err
		}
		scratch := [1]byte{}
		if _, ferr := c.feed(b, scratch[:]); ferr != nil {
			return ferr
		}
	}
	return nil
}

// Read implements io.Reader. Returned bytes are data only; IAC commands
// are consumed and replied to as a side effect.
//
// Read blocks until at least one data byte is available, then drains the
// underlying buffered reader without further blocking. Callers that want
// line-oriented input should layer a bufio.Reader on top of Conn.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := 0
	for n < len(p) {
		// Once we have at least one data byte and the buffered reader is
		// empty, stop — reading more would block.
		if n > 0 && c.br.Buffered() == 0 {
			return n, nil
		}
		b, err := c.br.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		emitted, ferr := c.feed(b, p[n:])
		if ferr != nil {
			if n > 0 {
				return n, nil
			}
			return 0, ferr
		}
		n += emitted
	}
	return n, nil
}

// Write implements io.Writer. Any IAC bytes in p are doubled on the wire
// so the client doesn't misinterpret them as commands.
func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if !hasIAC(p) {
		return c.raw.Write(p)
	}
	// Slow path: copy with escaping.
	buf := make([]byte, 0, len(p)+8)
	for _, b := range p {
		buf = append(buf, b)
		if b == cmdIAC {
			buf = append(buf, cmdIAC)
		}
	}
	if _, err := c.raw.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func hasIAC(p []byte) bool {
	for _, b := range p {
		if b == cmdIAC {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// IAC state machine

type parseState int

const (
	stNormal parseState = iota
	stIAC
	stWILL
	stWONT
	stDO
	stDONT
	stSB
	stSBIAC
)

// feed processes one incoming byte. If the byte is data (after IAC
// stripping), it is copied into out and the function returns 1. If it is
// consumed by the protocol, it returns 0.
func (c *Conn) feed(b byte, out []byte) (int, error) {
	switch c.parseState {
	case stNormal:
		if b == cmdIAC {
			c.parseState = stIAC
			return 0, nil
		}
		if len(out) == 0 {
			// Caller's buffer is full; this shouldn't happen because Read
			// calls feed with a non-empty slice, but guard anyway.
			return 0, io.ErrShortBuffer
		}
		out[0] = b
		return 1, nil

	case stIAC:
		switch b {
		case cmdIAC: // doubled IAC = literal 0xFF byte
			c.parseState = stNormal
			if len(out) == 0 {
				return 0, io.ErrShortBuffer
			}
			out[0] = cmdIAC
			return 1, nil
		case cmdWILL:
			c.parseState = stWILL
		case cmdWONT:
			c.parseState = stWONT
		case cmdDO:
			c.parseState = stDO
		case cmdDONT:
			c.parseState = stDONT
		case cmdSB:
			c.parseState = stSB
			c.sbOption = 0
			c.sbBuf = c.sbBuf[:0]
		case cmdNOP:
			c.parseState = stNormal
		case cmdAYT:
			c.parseState = stNormal
			c.replyAYT()
		default:
			c.parseState = stNormal
		}
		return 0, nil

	case stWILL:
		c.handleWILL(b)
		c.parseState = stNormal
		return 0, nil
	case stWONT:
		c.handleWONT(b)
		c.parseState = stNormal
		return 0, nil
	case stDO:
		c.handleDO(b)
		c.parseState = stNormal
		return 0, nil
	case stDONT:
		c.handleDONT(b)
		c.parseState = stNormal
		return 0, nil

	case stSB:
		if c.sbOption == 0 {
			c.sbOption = b
			return 0, nil
		}
		if b == cmdIAC {
			c.parseState = stSBIAC
			return 0, nil
		}
		c.sbBuf = append(c.sbBuf, b)
		return 0, nil
	case stSBIAC:
		if b == cmdSE {
			c.handleSB(c.sbOption, c.sbBuf)
			c.parseState = stNormal
			c.sbBuf = c.sbBuf[:0]
			c.sbOption = 0
			return 0, nil
		}
		if b == cmdIAC { // literal IAC inside SB
			c.sbBuf = append(c.sbBuf, cmdIAC)
			c.parseState = stSB
			return 0, nil
		}
		// Unexpected; abandon the SB.
		c.parseState = stNormal
		c.sbBuf = c.sbBuf[:0]
		c.sbOption = 0
		return 0, nil
	}
	return 0, errors.New("telnet: invalid parser state")
}

// ---------------------------------------------------------------------------
// option negotiation

func (c *Conn) sendInitialOffers(opts Options) error {
	var buf []byte
	if opts.OfferSGA {
		buf = append(buf, cmdIAC, cmdWILL, optSGA)
	}
	if opts.OfferTTYPE {
		buf = append(buf, cmdIAC, cmdDO, optTTYPE)
	}
	if opts.OfferNAWS {
		buf = append(buf, cmdIAC, cmdDO, optNAWS)
	}
	if opts.OfferCHARSET {
		buf = append(buf, cmdIAC, cmdWILL, optCHARSET)
	}
	if len(buf) == 0 {
		return nil
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.raw.Write(buf)
	return err
}

func (c *Conn) handleWILL(opt byte) {
	switch opt {
	case optTTYPE:
		// Client agreed to send TTYPE. Request the value.
		c.send(cmdIAC, cmdSB, optTTYPE, ttypeSEND, cmdIAC, cmdSE)
	case optNAWS, optSGA:
		// Client offers; we accept silently (we already DO'd them).
	case optCHARSET:
		c.send(cmdIAC, cmdDO, optCHARSET)
	default:
		c.send(cmdIAC, cmdDONT, opt)
	}
}

func (c *Conn) handleWONT(opt byte) {
	// Client refuses an option. Confirm.
	c.send(cmdIAC, cmdDONT, opt)
}

func (c *Conn) handleDO(opt byte) {
	switch opt {
	case optEcho, optSGA, optCHARSET:
		c.send(cmdIAC, cmdWILL, opt)
	default:
		c.send(cmdIAC, cmdWONT, opt)
	}
}

func (c *Conn) handleDONT(opt byte) {
	c.send(cmdIAC, cmdWONT, opt)
}

func (c *Conn) handleSB(opt byte, data []byte) {
	switch opt {
	case optTTYPE:
		// Expect: IS <name>
		if len(data) >= 1 && data[0] == ttypeIS {
			name := string(data[1:])
			c.smu.Lock()
			c.state.TermType = name
			c.smu.Unlock()
		}
	case optNAWS:
		// 4 bytes: width-hi, width-lo, height-hi, height-lo
		if len(data) >= 4 {
			w := int(data[0])<<8 | int(data[1])
			h := int(data[2])<<8 | int(data[3])
			c.smu.Lock()
			c.state.Width = w
			c.state.Height = h
			c.smu.Unlock()
		}
	case optCHARSET:
		// Minimum response: send REJ for now. Engine doesn't rely on this.
		if len(data) >= 1 && data[0] == chrstREQ {
			c.send(cmdIAC, cmdSB, optCHARSET, chrstREJ, cmdIAC, cmdSE)
		}
	}
}

func (c *Conn) replyAYT() {
	// Standard reply: a string. Keep it short.
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.raw.Write([]byte("\r\n[wintermute]\r\n"))
}

func (c *Conn) send(b ...byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.raw.Write(b)
}
