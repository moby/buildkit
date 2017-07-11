/*
Package locker is a simple package to manage named ReadWrite mutexes. These
appear to be especially useful for synchronizing access to session based
information in web applications.

The common use case is to use the package level functions, which use a package
level set of locks (safe to use from multiple goroutines simultaneously).
However, you may also create a new separate set of locks.

All locks are implemented with read-write mutexes. To use them like a regular
mutex, simply ignore the RLock/RUnlock functions.
*/
package locker

// BUG(burntsushi): The locker here can grow without bound in long running
// programs. Since it's intended to be used in web applications, this is a
// major problem. Figure out a way to keep the locker lean.

import (
	"fmt"
	"sync"
)

// Locker represents the set of named ReadWrite mutexes. It is safe to access
// from multiple goroutines simultaneously.
type Locker struct {
	locks   map[string]*sync.RWMutex
	locksRW *sync.RWMutex
}

var locker *Locker

func init() {
	locker = NewLocker()
}

func Lock(key string)    { locker.Lock(key) }
func Unlock(key string)  { locker.Unlock(key) }
func RLock(key string)   { locker.RLock(key) }
func RUnlock(key string) { locker.RUnlock(key) }

func NewLocker() *Locker {
	return &Locker{
		locks:   make(map[string]*sync.RWMutex),
		locksRW: new(sync.RWMutex),
	}
}

func (lker *Locker) Lock(key string) {
	lk, ok := lker.getLock(key)
	if !ok {
		lk = lker.newLock(key)
	}
	lk.Lock()
}

func (lker *Locker) Unlock(key string) {
	lk, ok := lker.getLock(key)
	if !ok {
		panic(fmt.Errorf("BUG: Lock for key '%s' not initialized.", key))
	}
	lk.Unlock()
}

func (lker *Locker) RLock(key string) {
	lk, ok := lker.getLock(key)
	if !ok {
		lk = lker.newLock(key)
	}
	lk.RLock()
}

func (lker *Locker) RUnlock(key string) {
	lk, ok := lker.getLock(key)
	if !ok {
		panic(fmt.Errorf("BUG: Lock for key '%s' not initialized.", key))
	}
	lk.RUnlock()
}

func (lker *Locker) newLock(key string) *sync.RWMutex {
	lker.locksRW.Lock()
	defer lker.locksRW.Unlock()

	lk := new(sync.RWMutex)
	lker.locks[key] = lk
	return lk
}

func (lker *Locker) getLock(key string) (*sync.RWMutex, bool) {
	lker.locksRW.RLock()
	defer lker.locksRW.RUnlock()

	lock, ok := lker.locks[key]
	return lock, ok
}

func (lker *Locker) deleteLock(key string) {
	lker.locksRW.Lock()
	defer lker.locksRW.Unlock()

	if _, ok := lker.locks[key]; ok {
		delete(lker.locks, key)
	}
}
