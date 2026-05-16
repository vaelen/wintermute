// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

// SessionBinding is the minimal session-side surface the engage helpers
// need. The concrete *session.Session satisfies it; tests use a fake.
type SessionBinding interface {
	Engagement() *Engagement
	SetEngagement(*Engagement)
}

// OpenForSession is the canonical way to start an engagement: open the
// registry entry, then attach it to the session. If Open fails (e.g.
// ErrHostBusy or ErrAlreadyEngaged), the session is left unchanged.
func OpenForSession(r *Registry, s SessionBinding, host *Host, h Handler, p *Participant) (*Engagement, error) {
	eng, err := r.Open(host, h, p)
	if err != nil {
		return nil, err
	}
	s.SetEngagement(eng)
	return eng, nil
}

// CloseForSession is the canonical close path. Looks up the current
// engagement, closes it in the registry, then clears the session's
// pointer. No-op if the session has no current engagement.
func CloseForSession(r *Registry, s SessionBinding, reason CloseReason) {
	eng := s.Engagement()
	if eng == nil {
		return
	}
	r.Close(eng, reason)
	s.SetEngagement(nil)
}
