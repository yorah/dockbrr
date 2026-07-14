// Package job is dockbrr's Job Engine: a persisted, per-project-serialized
// queue that is the ONLY component permitted to mutate Docker.
package job

import "sync"

// keyedMutex is a set of mutexes keyed by project id. Locking a key blocks only
// callers holding the same key, so jobs on different projects run concurrently
// while jobs on the same project serialize: the one-in-flight-per-project
// safety invariant. Entries are never reclaimed; the key space is bounded by the
// number of projects, so growth is negligible.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[int64]*sync.Mutex)}
}

// Lock acquires the mutex for key, creating it on first use.
func (k *keyedMutex) Lock(key int64) {
	k.mu.Lock()
	m, ok := k.locks[key]
	if !ok {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()
	m.Lock()
}

// Unlock releases the mutex for key. Unlocking a key that was never locked is a
// no-op.
func (k *keyedMutex) Unlock(key int64) {
	k.mu.Lock()
	m := k.locks[key]
	k.mu.Unlock()
	if m != nil {
		m.Unlock()
	}
}
