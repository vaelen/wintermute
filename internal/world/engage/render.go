// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import "strings"

// ExpandTemplate substitutes the supported placeholders ({{player}} and
// {{host}}) in t. Unknown placeholders are left as-is.
func ExpandTemplate(t, player, host string) string {
	t = strings.ReplaceAll(t, "{{player}}", player)
	t = strings.ReplaceAll(t, "{{host}}", host)
	return t
}
