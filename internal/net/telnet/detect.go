// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package telnet

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"time"
)

// Detect peeks the initial bytes from br to decide whether the connection
// uses telnet. The bytes are not consumed from br; subsequent reads will
// still observe them, so callers can hand the same buffered reader either
// to Wrap (when telnet is detected) or to the raw input pipeline (when it
// is not).
//
// Detect returns false if no bytes arrive within timeout. The read
// deadline is set on conn for the duration of the call and cleared on
// return.
func Detect(ctx context.Context, conn net.Conn, br *bufio.Reader, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return false, err
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	peeked, err := br.Peek(1)
	if len(peeked) == 0 {
		if isTimeoutErr(err) || errors.Is(err, context.DeadlineExceeded) {
			// No bytes within window → not telnet (or at least, not
			// proactively negotiating).
			return false, nil
		}
		// Other errors propagate.
		if err != nil {
			return false, err
		}
		return false, nil
	}
	return peeked[0] == cmdIAC, nil
}

func isTimeoutErr(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, os.ErrDeadlineExceeded)
}
