// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"encoding/json"
	"testing"
)

func TestKindMenuTerminalConstant(t *testing.T) {
	if KindMenuTerminal != "menu_terminal" {
		t.Errorf("KindMenuTerminal = %q, want %q", KindMenuTerminal, "menu_terminal")
	}
}

func TestMenuEntry_features(t *testing.T) {
	e := MenuEntry{Feature: FeatureFiles, Area: "dropbox"}
	if e.Feature != "files" {
		t.Errorf("FeatureFiles = %q, want %q", e.Feature, "files")
	}
	if FeatureMail != "mail" {
		t.Errorf("FeatureMail = %q, want %q", FeatureMail, "mail")
	}
	if FeatureBoards != "boards" {
		t.Errorf("FeatureBoards = %q, want %q", FeatureBoards, "boards")
	}
}

func TestDecodePolicyJSON_emptyJSON_returnsZero(t *testing.T) {
	p, menu, err := decodePolicyJSON("")
	if err != nil {
		t.Fatalf("decodePolicyJSON(\"\"): %v", err)
	}
	if p != (Policy{}) {
		t.Errorf("Policy = %+v, want zero", p)
	}
	if menu != nil {
		t.Errorf("menu = %+v, want nil", menu)
	}
}

func TestDecodePolicyJSON_policyOnly_backwardsCompatible(t *testing.T) {
	raw := `{"visibleActivity":true,"audibleContent":false,"joinable":true}`
	p, menu, err := decodePolicyJSON(raw)
	if err != nil {
		t.Fatalf("decodePolicyJSON: %v", err)
	}
	if !p.VisibleActivity || p.AudibleContent || !p.Joinable {
		t.Errorf("Policy = %+v, want VisibleActivity=true Joinable=true", p)
	}
	if menu != nil {
		t.Errorf("menu = %v, want nil", menu)
	}
}

func TestDecodePolicyJSON_menuAlongsidePolicy(t *testing.T) {
	raw := `{"visibleActivity":true,"menu":[` +
		`{"feature":"mail"},` +
		`{"feature":"boards"},` +
		`{"feature":"files","area":"dropbox"}` +
		`]}`
	p, menu, err := decodePolicyJSON(raw)
	if err != nil {
		t.Fatalf("decodePolicyJSON: %v", err)
	}
	if !p.VisibleActivity {
		t.Errorf("Policy.VisibleActivity = false, want true")
	}
	want := []MenuEntry{
		{Feature: "mail"},
		{Feature: "boards"},
		{Feature: "files", Area: "dropbox"},
	}
	if len(menu) != len(want) {
		t.Fatalf("menu has %d entries, want %d (%+v)", len(menu), len(want), menu)
	}
	for i, e := range want {
		if menu[i] != e {
			t.Errorf("menu[%d] = %+v, want %+v", i, menu[i], e)
		}
	}
}

func TestEncodePolicyJSON_omitsEmptyMenu(t *testing.T) {
	out, err := EncodePolicyJSON(Policy{VisibleActivity: true}, nil)
	if err != nil {
		t.Fatalf("EncodePolicyJSON: %v", err)
	}
	// Round-trip: decoding should give us back the same Policy and nil menu.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatalf("encoded JSON not valid: %v", err)
	}
	if _, present := probe["menu"]; present {
		t.Errorf("encoded JSON %q contains 'menu' but menu was nil", out)
	}
}

func TestEncodePolicyJSON_roundTripWithMenu(t *testing.T) {
	in := []MenuEntry{
		{Feature: "mail"},
		{Feature: "files", Area: "dropbox"},
	}
	out, err := EncodePolicyJSON(Policy{Joinable: true}, in)
	if err != nil {
		t.Fatalf("EncodePolicyJSON: %v", err)
	}
	p, menu, err := decodePolicyJSON(out)
	if err != nil {
		t.Fatalf("decodePolicyJSON: %v", err)
	}
	if !p.Joinable {
		t.Errorf("round-trip Policy.Joinable = false, want true")
	}
	if len(menu) != len(in) {
		t.Fatalf("round-trip menu = %+v, want %+v", menu, in)
	}
	for i := range in {
		if menu[i] != in[i] {
			t.Errorf("menu[%d] = %+v, want %+v", i, menu[i], in[i])
		}
	}
}

func TestValidateMenu_okEmpty(t *testing.T) {
	if err := ValidateMenu(nil); err != nil {
		t.Errorf("ValidateMenu(nil) = %v, want nil", err)
	}
}

func TestValidateMenu_okThreeFeatures(t *testing.T) {
	err := ValidateMenu([]MenuEntry{
		{Feature: FeatureMail},
		{Feature: FeatureBoards},
		{Feature: FeatureFiles, Area: "dropbox"},
	})
	if err != nil {
		t.Errorf("ValidateMenu: %v", err)
	}
}

func TestValidateMenu_rejectsUnknownFeature(t *testing.T) {
	err := ValidateMenu([]MenuEntry{{Feature: "wat"}})
	if err == nil {
		t.Fatal("expected error for unknown feature")
	}
}

func TestValidateMenu_filesRequiresArea(t *testing.T) {
	err := ValidateMenu([]MenuEntry{{Feature: FeatureFiles}})
	if err == nil {
		t.Fatal("expected error for files entry without area")
	}
}

func TestValidateMenu_nonFilesRejectsArea(t *testing.T) {
	err := ValidateMenu([]MenuEntry{{Feature: FeatureMail, Area: "x"}})
	if err == nil {
		t.Fatal("expected error for non-files entry with area")
	}
}
