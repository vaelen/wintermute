// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// Pool keeps a fixed-size set of warm Lua VMs ready for admin scripts.
// Each VM has the wintermute.* API pre-loaded; Put resets the table to a
// known shape before returning the VM to the pool so leaked globals from
// one script cannot bleed into the next.
//
// The pool is intentionally not bounded by a true semaphore: when every
// VM is checked out, Get creates a fresh one. This keeps the admin
// scripting path responsive under contention, at the cost of paying VM
// init cost in the (rare) overflow case. M8's player-tier pool will be
// strictly bounded.
type Pool struct {
	mu     sync.Mutex
	free   []*lua.LState
	size   int
	api    *API
	closed bool

	// execMu serializes Lua execution across every VM the pool hands
	// out. Tool callbacks registered from one script carry a pointer
	// to that script's VM globals via their Env (gopher-lua Lua
	// closures, unlike LGFunction Go callbacks, are NOT state-
	// independent), so running them on a different VM concurrently
	// with another script on the originating VM is a data race.
	// Holding execMu around every Run / Invoke makes that race
	// impossible without resorting to dump+load (which gopher-lua
	// 1.1.2 does not support — string.dump raises an error).
	// Coarser than necessary; M7 will revisit when NPC tools demand
	// real concurrency.
	execMu sync.Mutex
}

// PoolConfig is the input for NewPool. Size is the number of warm VMs
// to keep around; it defaults to 4 when zero.
type PoolConfig struct {
	Size int
	API  *API
}

// NewPool constructs a pool and pre-warms its VMs. The API is what each
// VM's wintermute table is bound against.
func NewPool(cfg PoolConfig) *Pool {
	size := cfg.Size
	if size <= 0 {
		size = 4
	}
	p := &Pool{
		size: size,
		api:  cfg.API,
	}
	p.free = make([]*lua.LState, 0, size)
	for i := 0; i < size; i++ {
		p.free = append(p.free, p.newVM())
	}
	return p
}

// Get returns a VM ready for use. The caller must Put it back when
// finished. When the pool is exhausted, a fresh VM is constructed.
func (p *Pool) Get() *lua.LState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.free); n > 0 {
		L := p.free[n-1]
		p.free[n-1] = nil
		p.free = p.free[:n-1]
		return L
	}
	return p.newVM()
}

// Put returns a VM to the pool. The wintermute global is re-bound to a
// fresh table so any mutation by the previous script is wiped. VMs in
// excess of the pool's size, or returned after Close, are closed in
// place rather than re-cached — otherwise a Put racing with shutdown
// would leak gopher-lua state.
func (p *Pool) Put(L *lua.LState) {
	if L == nil {
		return
	}
	p.api.Reset(L)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.free) >= p.size {
		L.Close()
		return
	}
	p.free = append(p.free, L)
}

// Close releases every pooled VM. Marks the pool closed so subsequent
// Put calls close the returned VM rather than re-cache it. Get on a
// closed pool still returns a fresh VM — typical lifetime is once-per-
// process and re-opening the pool is not exercised.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, L := range p.free {
		if L != nil {
			L.Close()
		}
	}
	p.free = p.free[:0]
}

// Stats describes the pool's current state. Useful for the @pool admin
// command that the M5 doc calls for.
type Stats struct {
	Size     int // capacity
	Cached   int // VMs currently sitting in the pool
}

// Stats returns a snapshot of the pool's state.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Size: p.size, Cached: len(p.free)}
}

// newVM constructs a single VM with the wintermute table bound.
func (p *Pool) newVM() *lua.LState {
	L := lua.NewState()
	p.api.Bind(L)
	return L
}
