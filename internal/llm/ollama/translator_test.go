// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package ollama

import (
	"testing"

	"github.com/vaelen/wintermute/internal/llm"
)

func TestToAPIToolsEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   []llm.ToolDef
	}{
		{name: "nil", in: nil},
		{name: "empty", in: []llm.ToolDef{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toAPITools(tc.in)
			if got != nil {
				t.Fatalf("toAPITools(%v) = %v, want nil", tc.in, got)
			}
		})
	}
}

func TestToAPIToolsSimpleSchema(t *testing.T) {
	tools := []llm.ToolDef{{
		Name:        "do_thing",
		Description: "does a thing",
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"x"},
			"properties": map[string]any{
				"x": map[string]any{"type": "string", "description": "the x"},
				"n": map[string]any{"type": "integer"},
			},
		},
	}}

	got := toAPITools(tools)
	if len(got) != 1 {
		t.Fatalf("len(toAPITools) = %d, want 1", len(got))
	}
	tool := got[0]
	if tool.Type != "function" {
		t.Errorf("Type = %q, want %q", tool.Type, "function")
	}
	if tool.Function.Name != "do_thing" {
		t.Errorf("Function.Name = %q, want %q", tool.Function.Name, "do_thing")
	}
	if tool.Function.Description != "does a thing" {
		t.Errorf("Function.Description = %q, want %q", tool.Function.Description, "does a thing")
	}

	params := tool.Function.Parameters
	if params.Type != "object" {
		t.Errorf("Parameters.Type = %q, want %q", params.Type, "object")
	}
	if len(params.Required) != 1 || params.Required[0] != "x" {
		t.Errorf("Parameters.Required = %v, want [\"x\"]", params.Required)
	}
	if params.Properties == nil {
		t.Fatalf("Parameters.Properties is nil")
	}

	x, ok := params.Properties.Get("x")
	if !ok {
		t.Fatalf("property \"x\" missing")
	}
	if len(x.Type) != 1 || x.Type[0] != "string" {
		t.Errorf("x.Type = %v, want [\"string\"]", x.Type)
	}
	if x.Description != "the x" {
		t.Errorf("x.Description = %q, want %q", x.Description, "the x")
	}

	n, ok := params.Properties.Get("n")
	if !ok {
		t.Fatalf("property \"n\" missing")
	}
	if len(n.Type) != 1 || n.Type[0] != "integer" {
		t.Errorf("n.Type = %v, want [\"integer\"]", n.Type)
	}
}

func TestToAPIToolsEnum(t *testing.T) {
	tools := []llm.ToolDef{{
		Name: "pick",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"color": map[string]any{
					"type": "string",
					"enum": []any{"red", "green", "blue"},
				},
			},
		},
	}}

	got := toAPITools(tools)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	color, ok := got[0].Function.Parameters.Properties.Get("color")
	if !ok {
		t.Fatalf("property \"color\" missing")
	}
	if len(color.Enum) != 3 {
		t.Fatalf("color.Enum len = %d, want 3", len(color.Enum))
	}
	want := []string{"red", "green", "blue"}
	for i, v := range color.Enum {
		s, ok := v.(string)
		if !ok || s != want[i] {
			t.Errorf("color.Enum[%d] = %v, want %q", i, v, want[i])
		}
	}
}

func TestToAPIToolsRequiredOmitted(t *testing.T) {
	tools := []llm.ToolDef{{
		Name: "no_required",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"x": map[string]any{"type": "string"},
			},
		},
	}}

	got := toAPITools(tools)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if len(got[0].Function.Parameters.Required) != 0 {
		t.Errorf("Required = %v, want empty", got[0].Function.Parameters.Required)
	}
}

func TestToAPIToolsEmptyProperties(t *testing.T) {
	tools := []llm.ToolDef{{
		Name: "no_props",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}}

	got := toAPITools(tools)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	params := got[0].Function.Parameters
	if params.Properties == nil {
		t.Fatalf("Properties is nil")
	}
	if got := params.Properties.ToMap(); len(got) != 0 {
		t.Errorf("Properties.ToMap() = %v, want empty", got)
	}
}

func TestToAPIToolsMalformedSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
	}{
		{
			name: "properties not a map",
			schema: map[string]any{
				"type":       "object",
				"properties": "not a map",
			},
		},
		{
			name: "property entry not a map",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bad": "not a map",
				},
			},
		},
		{
			name: "required wrong type",
			schema: map[string]any{
				"type":     "object",
				"required": "x",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("translator panicked: %v", r)
				}
			}()
			got := toAPITools([]llm.ToolDef{{Name: "t", Schema: tc.schema}})
			if len(got) != 1 {
				t.Fatalf("len = %d, want 1", len(got))
			}
		})
	}
}

func TestSchemaToParametersRequiredAsStringSlice(t *testing.T) {
	params := schemaToParameters(map[string]any{
		"type":     "object",
		"required": []string{"a", "b"},
	})
	if got, want := params.Required, []string{"a", "b"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Required = %v, want %v", got, want)
	}
}

func TestToolPropertyFromMapNil(t *testing.T) {
	p := toolPropertyFromMap(nil)
	if len(p.Type) != 0 || p.Description != "" || p.Enum != nil {
		t.Errorf("toolPropertyFromMap(nil) = %+v, want zero value", p)
	}
}

func TestToolPropertyFromMapTypeVariants(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want []string
	}{
		{
			name: "string",
			in:   map[string]any{"type": "string"},
			want: []string{"string"},
		},
		{
			name: "[]string",
			in:   map[string]any{"type": []string{"string", "null"}},
			want: []string{"string", "null"},
		},
		{
			name: "[]any with strings",
			in:   map[string]any{"type": []any{"string", "null"}},
			want: []string{"string", "null"},
		},
		{
			name: "[]any with non-string entries skipped",
			in:   map[string]any{"type": []any{"string", 1, "null"}},
			want: []string{"string", "null"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := toolPropertyFromMap(tc.in)
			if len(p.Type) != len(tc.want) {
				t.Fatalf("Type = %v, want %v", p.Type, tc.want)
			}
			for i, s := range tc.want {
				if string(p.Type[i]) != s {
					t.Errorf("Type[%d] = %q, want %q", i, p.Type[i], s)
				}
			}
		})
	}
}

func TestChatRequestBuildsToolsThroughTranslator(t *testing.T) {
	c, err := New(map[string]any{"model": "test-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cc, ok := c.(*client)
	if !ok {
		t.Fatalf("New returned %T, want *client", c)
	}
	if cc.model != "test-model" {
		t.Fatalf("client.model = %q, want %q", cc.model, "test-model")
	}

	tools := []llm.ToolDef{{
		Name:        "say",
		Description: "say a word",
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"word"},
			"properties": map[string]any{
				"word": map[string]any{"type": "string", "description": "the word"},
			},
		},
	}}

	apiTools := toAPITools(tools)
	if len(apiTools) != 1 {
		t.Fatalf("len(apiTools) = %d, want 1", len(apiTools))
	}
	if apiTools[0].Function.Name != "say" {
		t.Errorf("Function.Name = %q, want %q", apiTools[0].Function.Name, "say")
	}
	word, ok := apiTools[0].Function.Parameters.Properties.Get("word")
	if !ok {
		t.Fatalf("property \"word\" missing")
	}
	if word.Description != "the word" {
		t.Errorf("word.Description = %q, want %q", word.Description, "the word")
	}
	if len(apiTools[0].Function.Parameters.Required) != 1 || apiTools[0].Function.Parameters.Required[0] != "word" {
		t.Errorf("Required = %v, want [\"word\"]", apiTools[0].Function.Parameters.Required)
	}
}
