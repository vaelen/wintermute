// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	"context"
	"fmt"
	"sort"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// ToolEntry is a single registered tool: the metadata an NPC config can
// reference, plus the Lua closure that implements it.
type ToolEntry struct {
	Name        string
	Description string
	// Schema is a JSON-schema-ish description of the tool's arguments
	// (a Go map decoded from the Lua table). The keys we honour today
	// are "type" ("object") and "properties" (map of name → {type,
	// required}). Unknown keys are passed through untouched.
	Schema map[string]any

	// callback is the Lua function that implements the tool. It is
	// invoked on a freshly-Get'd VM from the registry's pool (NOT the
	// VM the script was registered from, which may already be back in
	// the pool by the time the tool is invoked). The closure is taken
	// by reference to the function; gopher-lua does not require that
	// LFunction live on any specific LState.
	callback *lua.LFunction
}

// ToolRegistry holds the in-memory mapping of name → tool. Tools are
// not persisted; admin scripts re-register them on every boot.
type ToolRegistry struct {
	mu sync.RWMutex
	m  map[string]*ToolEntry
}

// NewToolRegistry returns an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{m: map[string]*ToolEntry{}}
}

// Register installs (or replaces) a tool by name. Replacing an existing
// tool is allowed so admins can iterate without an explicit unregister.
func (r *ToolRegistry) Register(entry *ToolEntry) error {
	if entry == nil || entry.Name == "" {
		return fmt.Errorf("tool: name required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[entry.Name] = entry
	return nil
}

// Unregister removes a tool by name. Returns false if the tool was not
// present.
func (r *ToolRegistry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[name]; !ok {
		return false
	}
	delete(r.m, name)
	return true
}

// Get returns the entry for name, or nil.
func (r *ToolRegistry) Get(name string) *ToolEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[name]
}

// Names returns the registered tool names in lexicographic order.
func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Invoke runs the named tool with the given args. Returns the Lua
// return value(s) decoded back to a Go map (the conventional shape an
// NPC tool returns: {ok=true, message="..."}). Validation is performed
// against the entry's Schema before invocation.
//
// The invocation borrows a VM from pool, runs the callback on it, then
// returns the VM. This is intentional: the VM the tool was originally
// registered on may have been returned to the pool and reset by now;
// gopher-lua's LFunction is portable across states constructed from
// the same lua.NewState path.
func (r *ToolRegistry) Invoke(ctx context.Context, pool *Pool, name string, args map[string]any) (map[string]any, error) {
	entry := r.Get(name)
	if entry == nil {
		return nil, fmt.Errorf("wintermute: not_found: tool:%s", name)
	}
	if err := validateAgainstSchema(args, entry.Schema); err != nil {
		return nil, fmt.Errorf("wintermute: invalid_argument: %s", err)
	}
	L := pool.Get()
	defer pool.Put(L)

	luaArgs := goToLua(L, args)
	L.Push(entry.callback)
	L.Push(luaArgs)
	if err := L.PCall(1, 1, nil); err != nil {
		return nil, luaError(err)
	}
	ret := L.Get(-1)
	L.Pop(1)
	out, ok := luaToGo(ret).(map[string]any)
	if !ok {
		return map[string]any{"ok": true, "value": luaToGo(ret)}, nil
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// schema validation

// validateAgainstSchema enforces a minimal subset of JSON Schema:
//   - type "object" requires args to be a map (always true at this layer).
//   - properties.<name>.type "string"|"number"|"boolean" is checked when
//     the field is present.
//   - required is honoured: every name in `required` must appear in args.
//
// Unknown fields are allowed (lax). This is enough to catch typos in
// admin-authored tool args without pulling in a full JSON-Schema dep.
func validateAgainstSchema(args, schema map[string]any) error {
	if schema == nil {
		return nil
	}
	props, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, r := range required {
		name, ok := r.(string)
		if !ok {
			continue
		}
		if _, present := args[name]; !present {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	for name, def := range props {
		val, present := args[name]
		if !present {
			continue
		}
		defMap, ok := def.(map[string]any)
		if !ok {
			continue
		}
		want, _ := defMap["type"].(string)
		if want == "" {
			continue
		}
		if !typeMatches(want, val) {
			return fmt.Errorf("field %q: expected %s, got %T", name, want, val)
		}
	}
	return nil
}

func typeMatches(want string, v any) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		switch v.(type) {
		case float64, int, int64:
			return true
		}
		return false
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	}
	return true
}

// ---------------------------------------------------------------------------
// Lua bindings

func (a *API) luaToolRegister(L *lua.LState) int {
	name := L.CheckString(1)
	cb := L.CheckFunction(2)
	var schema map[string]any
	var desc string
	if L.GetTop() >= 3 {
		opts := L.CheckTable(3)
		desc = optString(opts, "description")
		if sch, ok := opts.RawGetString("schema").(*lua.LTable); ok {
			if m, ok := luaToGo(sch).(map[string]any); ok {
				schema = m
			}
		}
	}
	entry := &ToolEntry{
		Name:        name,
		Description: desc,
		Schema:      schema,
		callback:    cb,
	}
	if err := a.Tools.Register(entry); err != nil {
		return pushError(L, err)
	}
	L.Push(lua.LBool(true))
	return 1
}

func (a *API) luaToolUnregister(L *lua.LState) int {
	name := L.CheckString(1)
	ok := a.Tools.Unregister(name)
	L.Push(lua.LBool(ok))
	return 1
}

func (a *API) luaToolList(L *lua.LState) int {
	names := a.Tools.Names()
	t := L.NewTable()
	for i, n := range names {
		t.RawSetInt(i+1, lua.LString(n))
	}
	L.Push(t)
	return 1
}

// ---------------------------------------------------------------------------
// Lua <-> Go conversions

// goToLua converts a Go value into a Lua value. Used to pass tool args
// (which originate as Go maps from @invoke's JSON parsing) into Lua.
func goToLua(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(x)
	case bool:
		return lua.LBool(x)
	case int:
		return lua.LNumber(x)
	case int64:
		return lua.LNumber(x)
	case float64:
		return lua.LNumber(x)
	case map[string]any:
		t := L.NewTable()
		for k, val := range x {
			t.RawSetString(k, goToLua(L, val))
		}
		return t
	case []any:
		t := L.NewTable()
		for i, val := range x {
			t.RawSetInt(i+1, goToLua(L, val))
		}
		return t
	}
	return lua.LNil
}

// luaToGo converts a Lua value into a Go value. Used to read tool return
// values back into a Go map.
func luaToGo(v lua.LValue) any {
	switch x := v.(type) {
	case lua.LBool:
		return bool(x)
	case lua.LNumber:
		f := float64(x)
		if f == float64(int64(f)) {
			return int64(f)
		}
		return f
	case lua.LString:
		return string(x)
	case *lua.LTable:
		// Detect array-shaped tables (1..N keys) so we round-trip
		// `{1,2,3}` as []any rather than {"1": 1, "2": 2, ...}.
		n := x.Len()
		if n > 0 {
			arr := make([]any, 0, n)
			for i := 1; i <= n; i++ {
				arr = append(arr, luaToGo(x.RawGetInt(i)))
			}
			return arr
		}
		m := map[string]any{}
		x.ForEach(func(k, val lua.LValue) {
			if ks, ok := k.(lua.LString); ok {
				m[string(ks)] = luaToGo(val)
			}
		})
		return m
	}
	return nil
}
