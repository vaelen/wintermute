// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package llm

import (
	"fmt"
	"sort"
	"sync"
)

// Factory builds an LLM instance from a backend-specific options map. Backends
// register a Factory under a stable name (e.g. "ollama") in their init().
type Factory func(opts map[string]any) (LLM, error)

var (
	registryMu sync.Mutex
	registry   = map[string]Factory{}
)

// Register associates a backend name with its Factory. It panics on duplicate
// registration: backend init() runs once per process and a duplicate name
// almost always means two backends are claiming the same identity, which we
// want to surface immediately rather than silently overwriting.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[name]; ok {
		panic(fmt.Sprintf("llm: backend %q already registered", name))
	}
	registry[name] = f
}

// Open constructs an LLM for the given backend, returning an error naming the
// backend if it is not registered.
func Open(backend string, opts map[string]any) (LLM, error) {
	registryMu.Lock()
	f, ok := registry[backend]
	registryMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("llm: backend %q is not registered", backend)
	}
	return f(opts)
}

// Backends returns the registered backend names in alphabetical order.
func Backends() []string {
	registryMu.Lock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	registryMu.Unlock()
	sort.Strings(names)
	return names
}
