// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"encoding/json"
	"fmt"
)

// KindMenuTerminal hosts a line-drawn menu over the M6 mail/boards/files
// services. The per-object menu config lives in object_engage.policy under
// the "menu" key (see MenuEntry).
const KindMenuTerminal = "menu_terminal"

// Feature names accepted in a MenuEntry. The set is closed in M6.3; new
// feature kinds are added by registering an entry here and a submenu shim
// under internal/world/engage/menu/feature.
const (
	FeatureMail   = "mail"
	FeatureBoards = "boards"
	FeatureFiles  = "files"
)

// MenuEntry is one row of an object's menu configuration. Feature picks
// the submenu; Area is only used (and required) when Feature == FeatureFiles
// because each files entry targets exactly one area.
type MenuEntry struct {
	Feature string `json:"feature"`
	Area    string `json:"area,omitempty"`
}

// ValidateMenu enforces the M6.3 well-formedness rules. Empty / nil is
// valid (the host opens to a main menu with no feature rows). Returns a
// descriptive error on the first invalid entry.
func ValidateMenu(entries []MenuEntry) error {
	for i, e := range entries {
		switch e.Feature {
		case FeatureMail, FeatureBoards:
			if e.Area != "" {
				return fmt.Errorf("menu[%d]: feature %q does not accept area", i, e.Feature)
			}
		case FeatureFiles:
			if e.Area == "" {
				return fmt.Errorf("menu[%d]: feature %q requires area", i, e.Feature)
			}
		default:
			return fmt.Errorf("menu[%d]: unknown feature %q", i, e.Feature)
		}
	}
	return nil
}

// policyJSON is the wire shape of the object_engage.policy column. The
// Policy fields are kept at the top level (backwards compatible with
// pre-M6.3 rows that only stored Policy) and the optional Menu lives
// beside them.
type policyJSON struct {
	VisibleActivity bool        `json:"visibleActivity,omitempty"`
	AudibleContent  bool        `json:"audibleContent,omitempty"`
	Joinable        bool        `json:"joinable,omitempty"`
	Menu            []MenuEntry `json:"menu,omitempty"`
}

// decodePolicyJSON parses the raw column value into Policy + menu entries.
// An empty / "{}" string yields zero values without error.
func decodePolicyJSON(raw string) (Policy, []MenuEntry, error) {
	if raw == "" || raw == "{}" {
		return Policy{}, nil, nil
	}
	var pj policyJSON
	if err := json.Unmarshal([]byte(raw), &pj); err != nil {
		return Policy{}, nil, err
	}
	p := Policy{
		VisibleActivity: pj.VisibleActivity,
		AudibleContent:  pj.AudibleContent,
		Joinable:        pj.Joinable,
	}
	return p, pj.Menu, nil
}

// EncodePolicyJSON renders Policy + menu entries as the JSON to store in
// object_engage.policy.
func EncodePolicyJSON(p Policy, menu []MenuEntry) (string, error) {
	pj := policyJSON{
		VisibleActivity: p.VisibleActivity,
		AudibleContent:  p.AudibleContent,
		Joinable:        p.Joinable,
		Menu:            menu,
	}
	b, err := json.Marshal(pj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
