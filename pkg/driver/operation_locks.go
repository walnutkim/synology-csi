/*
 * Copyright 2021 Synology Inc.
 */

package driver

import "sync"

// operationLocks remembers the names that have an operation in progress.
type operationLocks struct {
	mu    sync.Mutex
	names map[string]struct{}
}

func newOperationLocks() *operationLocks {
	return &operationLocks{names: map[string]struct{}{}}
}

// tryLock claims name and reports whether it was free.
func (l *operationLocks) tryLock(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, busy := l.names[name]; busy {
		return false
	}
	l.names[name] = struct{}{}
	return true
}

func (l *operationLocks) unlock(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.names, name)
}
