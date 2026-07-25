package main

import "sync"

// restartFlag remembers that a config file was written since the last restart. ARK reads its INIs
// at boot and writes them back out afterwards, so an edit that is never followed by a restart is
// not merely inactive: it can be overwritten by the running server. The flag makes that impossible
// to forget without taking the choice of when away from the operator.
//
// Kept in memory on purpose. It is a reminder, not a fact about the world, and a panel restart
// losing it costs nothing: the marker reappears with the next save.
type restartFlag struct {
	mu      sync.Mutex
	pending bool
}

// The methods tolerate a nil receiver so a zero-value config, as used across the tests, behaves as
// "nothing pending" instead of panicking.

func (f *restartFlag) set() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = true
}

func (f *restartFlag) clear() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = false
}

func (f *restartFlag) get() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending
}
