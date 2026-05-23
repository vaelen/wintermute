// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package loop

import "github.com/vaelen/wintermute/internal/world/events"

// EngageLookup tells the loop which other-party object ids the NPC is
// currently engaged with, so the loop can drop say events whose Actor
// is not a participant in that engagement. The registry installs an
// adapter around internal/world/engage; tests may pass nil to disable
// filtering.
type EngageLookup interface {
	EngagedWith(npcID events.ObjectID) (participantIDs []events.ObjectID, engaged bool)
}
